package wechat

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	defaultOrigin = "https://ilinkai.weixin.qq.com"
	// Tencent openclaw-weixin package metadata and its 0x00MMNNPP encoding.
	channelVersion = "3.0.0-beta-1"
	appVersion     = "196608"
	pollTimeout    = 35 * time.Second
	requestTimeout = 15 * time.Second
	maxResponse    = 4 << 20
)

var (
	ErrSessionExpired = errors.New("WeChat session expired")
	ErrSendUncertain  = errors.New("WeChat delivery is unconfirmed")
	errNetwork        = errors.New("network request failed")
)

var wechatHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type Client struct {
	testOrigin string
}

func NewClient() *Client {
	return &Client{}
}

type Account struct {
	Token, BotID, OwnerID, BaseURL string
}

type QR struct {
	ID, Content string
}

type Scan struct {
	Status, RedirectHost string
	Account              Account
}

type Inbound struct {
	ID, From, To, Text, ContextToken string
}

type apiResult struct {
	Ret     int `json:"ret"`
	ErrCode int `json:"errcode"`
}

func (result apiResult) err(operation string) error {
	if result.Ret == -14 || result.ErrCode == -14 {
		return ErrSessionExpired
	}
	if result.Ret != 0 || result.ErrCode != 0 {
		return fmt.Errorf("WeChat %s was rejected (ret=%d, errcode=%d)", operation, result.Ret, result.ErrCode)
	}
	return nil
}

type baseInfo struct {
	ChannelVersion string `json:"channel_version"`
	BotAgent       string `json:"bot_agent"`
}

var clientInfo = baseInfo{ChannelVersion: channelVersion, BotAgent: "Orca-Wails"}

func (c *Client) StartQR(ctx context.Context) (QR, error) {
	const operation = "get_bot_qrcode"
	query := url.Values{"bot_type": {"3"}}
	body := struct {
		LocalTokens []string `json:"local_token_list"`
	}{LocalTokens: []string{}}
	var response struct {
		apiResult
		ID      string `json:"qrcode"`
		Content string `json:"qrcode_img_content"`
	}
	if err := c.request(ctx, requestTimeout, c.origin(), operation, query, "", body, &response); err != nil {
		return QR{}, err
	}
	if err := response.err(operation); err != nil {
		return QR{}, err
	}
	if strings.TrimSpace(response.ID) == "" || strings.TrimSpace(response.Content) == "" {
		return QR{}, errors.New("WeChat QR response was incomplete")
	}
	return QR{ID: response.ID, Content: response.Content}, nil
}

func (c *Client) PollQR(ctx context.Context, id, pin, redirectHost string) (Scan, error) {
	const operation = "get_qrcode_status"
	if strings.TrimSpace(id) == "" {
		return Scan{}, errors.New("WeChat QR identifier is required")
	}
	origin := c.origin()
	if redirectHost != "" {
		origin = "https://" + redirectHost
		if strings.Contains(redirectHost, "/") || !c.trustedOrigin(origin) {
			return Scan{}, errors.New("WeChat QR redirect host is invalid")
		}
	}
	query := url.Values{"qrcode": {id}}
	if pin != "" {
		query.Set("verify_code", pin)
	}
	var response struct {
		apiResult
		Status       string `json:"status"`
		RedirectHost string `json:"redirect_host"`
		Token        string `json:"bot_token"`
		BotID        string `json:"ilink_bot_id"`
		OwnerID      string `json:"ilink_user_id"`
		BaseURL      string `json:"baseurl"`
	}
	err := c.request(ctx, pollTimeout, origin, operation, query, "", nil, &response)
	if ctx.Err() == nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errNetwork)) {
		return Scan{Status: "wait"}, nil
	}
	if err != nil {
		return Scan{}, err
	}
	if err := response.err(operation); err != nil {
		return Scan{}, err
	}
	switch response.Status {
	case "confirmed":
		baseURL := response.BaseURL
		if baseURL == "" {
			baseURL = c.origin()
		}
		account := Account{Token: response.Token, BotID: response.BotID, OwnerID: response.OwnerID, BaseURL: baseURL}
		if _, err := c.accountOrigin(account); err != nil {
			return Scan{}, err
		}
		return Scan{Status: "confirmed", Account: account}, nil
	case "scaned_but_redirect":
		host := response.RedirectHost
		if host != "" && (strings.Contains(host, "/") || !c.trustedOrigin("https://"+host)) {
			return Scan{}, errors.New("WeChat QR redirect host is invalid")
		}
		return Scan{Status: response.Status, RedirectHost: host}, nil
	case "wait", "scaned", "expired", "verify_code_blocked", "binded_redirect", "need_verifycode", "confirmed_but_need_verifycode":
		return Scan{Status: response.Status}, nil
	default:
		if strings.Contains(response.Status, "need_verifycode") {
			return Scan{Status: "need_verifycode"}, nil
		}
		return Scan{}, errors.New("WeChat QR status was unrecognized")
	}
}

type textItem struct {
	Text string `json:"text"`
}

type messageItem struct {
	Type      int       `json:"type"`
	Text      *textItem `json:"text_item,omitempty"`
	Completed *bool     `json:"is_completed,omitempty"`
}

type message struct {
	From         string        `json:"from_user_id"`
	To           string        `json:"to_user_id"`
	ClientID     string        `json:"client_id,omitempty"`
	Type         int           `json:"message_type"`
	State        int           `json:"message_state"`
	ContextToken string        `json:"context_token,omitempty"`
	Items        []messageItem `json:"item_list"`
	MessageID    json.Number   `json:"message_id,omitempty"`
	Sequence     json.Number   `json:"seq,omitempty"`
	GroupID      string        `json:"group_id,omitempty"`
	DeletedAt    int64         `json:"delete_time_ms,omitempty"`
}

func (msg message) inbound() (Inbound, bool) {
	if msg.Type != 1 || msg.State != 2 || msg.GroupID != "" || msg.DeletedAt != 0 || msg.From == "" {
		return Inbound{}, false
	}
	parts := make([]string, 0, len(msg.Items))
	for _, item := range msg.Items {
		if item.Type == 1 && item.Text != nil && strings.TrimSpace(item.Text.Text) != "" &&
			(item.Completed == nil || *item.Completed) {
			parts = append(parts, item.Text.Text)
		}
	}
	if len(parts) == 0 {
		return Inbound{}, false
	}
	var id string
	if value, err := strconv.ParseUint(msg.MessageID.String(), 10, 64); err == nil && value != 0 {
		id = strconv.FormatUint(value, 10)
	} else if msg.ClientID != "" {
		id = "client:" + msg.ClientID
	} else if value, err := strconv.ParseUint(msg.Sequence.String(), 10, 64); err == nil && value != 0 {
		id = "sequence:" + strconv.FormatUint(value, 10)
	}
	if id == "" {
		return Inbound{}, false
	}
	return Inbound{ID: id, From: msg.From, To: msg.To, Text: strings.Join(parts, "\n"), ContextToken: msg.ContextToken}, true
}

func (c *Client) Updates(ctx context.Context, account Account, cursor string) ([]Inbound, string, error) {
	const operation = "getupdates"
	origin, err := c.accountOrigin(account)
	if err != nil {
		return nil, cursor, err
	}
	body := struct {
		Cursor string   `json:"get_updates_buf"`
		Base   baseInfo `json:"base_info"`
	}{Cursor: cursor, Base: clientInfo}
	var response struct {
		apiResult
		Cursor   string    `json:"get_updates_buf"`
		Messages []message `json:"msgs"`
	}
	err = c.request(ctx, pollTimeout, origin, operation, nil, account.Token, body, &response)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, cursor, nil
	}
	if err != nil {
		return nil, cursor, err
	}
	if err := response.err(operation); err != nil {
		return nil, cursor, err
	}
	if response.Cursor != "" {
		cursor = response.Cursor
	}
	var messages []Inbound
	for _, msg := range response.Messages {
		if inbound, ok := msg.inbound(); ok {
			messages = append(messages, inbound)
		}
	}
	return messages, cursor, nil
}

func (c *Client) Send(ctx context.Context, account Account, contextToken, clientID, text string) error {
	const operation = "sendmessage"
	origin, err := c.accountOrigin(account)
	if err != nil {
		return err
	}
	if strings.TrimSpace(contextToken) == "" || strings.TrimSpace(clientID) == "" || strings.TrimSpace(text) == "" {
		return errors.New("WeChat send requires a conversation context, message identifier, and text")
	}
	body := struct {
		Msg  message  `json:"msg"`
		Base baseInfo `json:"base_info"`
	}{
		Msg: message{
			To: account.OwnerID, ClientID: clientID, Type: 2, State: 2, ContextToken: contextToken,
			Items: []messageItem{{Type: 1, Text: &textItem{Text: text}}},
		},
		Base: clientInfo,
	}
	var response apiResult
	if err := c.request(ctx, requestTimeout, origin, operation, nil, account.Token, body, &response); err != nil {
		return err
	}
	return response.err(operation)
}

func (c *Client) origin() string {
	if c != nil && c.testOrigin != "" {
		return c.testOrigin
	}
	return defaultOrigin
}

func (c *Client) accountOrigin(account Account) (string, error) {
	if !validCredential(account.Token) || !validCredential(account.BotID) || !validCredential(account.OwnerID) {
		return "", errors.New("WeChat account credentials are invalid or incomplete")
	}
	origin := account.BaseURL
	if origin == "" {
		origin = c.origin()
	}
	if !c.trustedOrigin(origin) {
		return "", errors.New("WeChat account origin is invalid")
	}
	return origin, nil
}

func validCredential(value string) bool {
	return value != "" && strings.IndexFunc(value, func(r rune) bool {
		return r < '!' || r > '~'
	}) == -1
}

func (c *Client) trustedOrigin(origin string) bool {
	if c != nil && c.testOrigin != "" && origin == c.testOrigin {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(origin, "#") ||
		(parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") ||
		parsed.Host != parsed.Hostname() {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return strings.HasSuffix(host, ".weixin.qq.com")
}

func (c *Client) request(ctx context.Context, timeout time.Duration, origin, operation string, query url.Values, token string, body, response any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !c.trustedOrigin(origin) {
		return errors.New("WeChat API origin is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	endpoint := strings.TrimSuffix(origin, "/") + "/ilink/bot/" + operation
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	method := http.MethodGet
	var data []byte
	if body != nil {
		method = http.MethodPost
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("could not encode WeChat %s request", operation)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("could not build WeChat %s request", operation)
	}
	req.Header.Set("iLink-App-Id", "bot")
	req.Header.Set("iLink-App-ClientVersion", appVersion)
	if method == http.MethodPost {
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			return errors.New("could not generate WeChat request header")
		}
		uin := strconv.FormatUint(uint64(binary.BigEndian.Uint32(random[:])), 10)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(uin)))
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	var connected atomic.Bool
	if operation == "sendmessage" {
		req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GotConn: func(httptrace.GotConnInfo) { connected.Store(true) },
		}))
	}
	resp, err := wechatHTTP.Do(req)
	if err != nil {
		if operation == "sendmessage" {
			if !connected.Load() {
				return errNetwork
			}
			return fmt.Errorf("%w: request failed", ErrSendUncertain)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("WeChat %s: %w", operation, errNetwork)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if operation == "sendmessage" && resp.StatusCode >= 500 {
			return fmt.Errorf("%w: HTTP %d", ErrSendUncertain, resp.StatusCode)
		}
		return fmt.Errorf("WeChat %s returned HTTP %d", operation, resp.StatusCode)
	}

	result, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	trimmed := bytes.TrimSpace(result)
	if err != nil || len(result) > maxResponse || len(trimmed) == 0 || trimmed[0] != '{' ||
		json.Unmarshal(trimmed, response) != nil {
		if operation == "sendmessage" {
			return fmt.Errorf("%w: invalid response", ErrSendUncertain)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil {
			return fmt.Errorf("WeChat %s: %w", operation, errNetwork)
		}
		return fmt.Errorf("WeChat %s returned an invalid response", operation)
	}
	return nil
}

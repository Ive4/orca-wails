package wechat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientStartsSelfBind(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.RequestURI() != "/ilink/bot/get_bot_qrcode?bot_type=3" {
			t.Errorf("unexpected QR request %s %s", r.Method, r.URL.RequestURI())
		}
		checkAppHeaders(t, r)
		checkPostHeaders(t, r, "")
		var body map[string]json.RawMessage
		readRequest(t, r, &body)
		if len(body) != 1 || string(body["local_token_list"]) != "[]" {
			t.Errorf("unexpected QR body %v", body)
		}
		fmt.Fprint(w, `{"qrcode":"qr","qrcode_img_content":"https://example.com/qr"}`)
	})

	qr, err := client.StartQR(t.Context())
	if want := (QR{ID: "qr", Content: "https://example.com/qr"}); err != nil || qr != want {
		t.Fatalf("StartQR = %+v, %v, want %+v", qr, err, want)
	}
}

func TestClientPollsQRWithPINAndConfirmsAccount(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ilink/bot/get_qrcode_status" || r.URL.Query().Get("qrcode") != "qr+ /&" {
			t.Errorf("unexpected poll request %s %s", r.Method, r.URL.RequestURI())
		}
		checkAppHeaders(t, r)
		for _, name := range []string{"Authorization", "AuthorizationType", "X-WECHAT-UIN"} {
			if r.Header.Get(name) != "" {
				t.Errorf("QR poll sent optional POST header %s", name)
			}
		}
		switch r.URL.Query().Get("verify_code") {
		case "":
			fmt.Fprint(w, `{"status":"confirmed_but_need_verifycode"}`)
		case "001234":
			fmt.Fprint(w, `{"status":"confirmed","bot_token":"abc","ilink_bot_id":"bot","ilink_user_id":"owner"}`)
		default:
			t.Errorf("unexpected PIN %q", r.URL.Query().Get("verify_code"))
		}
	})

	scan, err := client.PollQR(t.Context(), "qr+ /&", "", "")
	if err != nil || scan.Status != "confirmed_but_need_verifycode" || scan.Account != (Account{}) {
		t.Fatalf("pending PollQR = %+v, %v", scan, err)
	}
	scan, err = client.PollQR(t.Context(), "qr+ /&", "001234", "")
	want := Account{Token: "abc", BotID: "bot", OwnerID: "owner", BaseURL: client.testOrigin}
	if err != nil || scan.Status != "confirmed" || scan.Account != want {
		t.Fatalf("confirmed PollQR = %+v, %v", scan, err)
	}
}

func TestClientRejectsUnsafeQRStatus(t *testing.T) {
	for name, body := range map[string]string{
		"missing owner":    `{"status":"confirmed","bot_token":"abc","ilink_bot_id":"bot"}`,
		"bad token":        `{"status":"confirmed","bot_token":"bad token","ilink_bot_id":"bot","ilink_user_id":"owner"}`,
		"bad handoff":      `{"status":"scaned_but_redirect","redirect_host":"ilinkai.weixin.qq.com.example.invalid"}`,
		"bad account base": `{"status":"confirmed","bot_token":"abc","ilink_bot_id":"bot","ilink_user_id":"owner","baseurl":"http://ilinkai.weixin.qq.com"}`,
	} {
		t.Run(name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) })
			scan, err := client.PollQR(t.Context(), "secret-qr", "secret-pin", "")
			if err == nil || scan != (Scan{}) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "bad token") {
				t.Fatalf("unsafe QR response yielded %+v, %v", scan, err)
			}
		})
	}
}

func TestClientAcceptsOfficialRegionalQRHost(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":"scaned_but_redirect","redirect_host":"regional.weixin.qq.com"}`)
	})
	scan, err := client.PollQR(t.Context(), "qr", "", "")
	if err != nil || scan != (Scan{Status: "scaned_but_redirect", RedirectHost: "regional.weixin.qq.com"}) {
		t.Fatalf("regional PollQR = %+v, %v", scan, err)
	}
}

func TestClientUpdatesDecodesFinishedPrivateText(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ilink/bot/getupdates" {
			t.Errorf("unexpected update request %s %s", r.Method, r.URL.RequestURI())
		}
		checkAppHeaders(t, r)
		checkPostHeaders(t, r, "Bearer local-token")
		var body struct {
			Cursor string            `json:"get_updates_buf"`
			Base   map[string]string `json:"base_info"`
		}
		readRequest(t, r, &body)
		if body.Cursor != "cursor-one" {
			t.Errorf("unexpected request cursor %q", body.Cursor)
		}
		checkBaseInfo(t, body.Base)
		fmt.Fprint(w, `{"ret":0,"errcode":0,"get_updates_buf":"cursor-two","longpolling_timeout_ms":35000,"msgs":[
			{"message_id":9007199254740993,"from_user_id":"owner","to_user_id":"bot","context_token":"context","message_type":1,"message_state":2,
			"item_list":[{"type":1,"text_item":{"text":"任务"}},{"type":2},{"type":1,"text_item":{"text":"ignored"},"is_completed":false},{"type":1,"text_item":{"text":"A17"},"is_completed":true}]},
			{"message_id":2,"from_user_id":"owner","to_user_id":"bot","message_type":1,"message_state":1,"item_list":[{"type":1,"text_item":{"text":"typing"}}]},
			{"message_id":3,"from_user_id":"bot","to_user_id":"owner","message_type":2,"message_state":2,"item_list":[{"type":1,"text_item":{"text":"outgoing"}}]},
			{"message_id":4,"from_user_id":"owner","to_user_id":"bot","message_type":1,"message_state":2,"item_list":[{"type":3,"voice_item":{"text":"voice"}}]},
			{"message_id":5,"from_user_id":"owner","to_user_id":"bot","message_type":1,"message_state":2,"group_id":"group","item_list":[{"type":1,"text_item":{"text":"group"}}]},
			{"message_id":6,"from_user_id":"owner","to_user_id":"bot","message_type":1,"message_state":2,"delete_time_ms":1,"item_list":[{"type":1,"text_item":{"text":"deleted"}}]}
		]}`)
	})

	msgs, cursor, err := client.Updates(t.Context(), testAccount(), "cursor-one")
	want := []Inbound{{ID: "9007199254740993", From: "owner", To: "bot", Text: "任务\nA17", ContextToken: "context"}}
	if err != nil || cursor != "cursor-two" || !reflect.DeepEqual(msgs, want) {
		t.Fatalf("Updates = %+v, %q, %v", msgs, cursor, err)
	}
}

func TestClientUpdatesPreservesCursorWithoutReplacement(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ret":0,"msgs":[]}`)
	})
	msgs, cursor, err := client.Updates(t.Context(), testAccount(), "previous")
	if err != nil || cursor != "previous" || len(msgs) != 0 {
		t.Fatalf("Updates = %+v, %q, %v", msgs, cursor, err)
	}
}

func TestClientSendsTextOnlyToBoundOwner(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/ilink/bot/sendmessage" {
			t.Errorf("unexpected send %s %s", r.Method, r.URL.RequestURI())
		}
		checkAppHeaders(t, r)
		checkPostHeaders(t, r, "Bearer local-token")
		var body struct {
			Msg  json.RawMessage   `json:"msg"`
			Base map[string]string `json:"base_info"`
		}
		readRequest(t, r, &body)
		checkBaseInfo(t, body.Base)
		var got, want map[string]any
		json.Unmarshal(body.Msg, &got)
		json.Unmarshal([]byte(`{"from_user_id":"","to_user_id":"owner","client_id":"operation","message_type":2,"message_state":2,"context_token":"context","item_list":[{"type":1,"text_item":{"text":"等待输入"}}]}`), &want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("message = %+v, want %+v", got, want)
		}
		fmt.Fprint(w, `{"ret":0}`)
	})
	if err := client.Send(t.Context(), testAccount(), "context", "operation", "等待输入"); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsUntrustedAccountOrigins(t *testing.T) {
	client := NewClient()
	for _, origin := range []string{
		"http://ilinkai.weixin.qq.com",
		"https://ilinkai.weixin.qq.com.example.invalid",
		"https://fakeweixin.qq.com",
		"https://ilinkai.weixin.qq.com:443",
		"https://private@ilinkai.weixin.qq.com",
		"https://ilinkai.weixin.qq.com/?other=private",
		"https://ilinkai.weixin.qq.com/#private",
		"https://ilinkai.weixin.qq.com/private",
		"https://127.0.0.1",
	} {
		account := testAccount()
		account.BaseURL = origin
		err := client.Send(t.Context(), account, "context", "operation", "text")
		if err == nil || errors.Is(err, ErrSendUncertain) || strings.Contains(err.Error(), origin) {
			t.Errorf("Send to %q yielded %v", origin, err)
		}
	}
}

func TestClientDoesNotFollowTokenBearingRedirect(t *testing.T) {
	var followed atomic.Bool
	client := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect-target" {
			followed.Store(true)
			fmt.Fprint(w, `{"ret":0}`)
			return
		}
		http.Redirect(w, r, "/redirect-target", http.StatusTemporaryRedirect)
	})
	err := client.Send(t.Context(), testAccount(), "context", "operation", "text")
	if err == nil || followed.Load() {
		t.Fatalf("Send error = %v, followed redirect = %v", err, followed.Load())
	}
}

func TestClientClassifiesSendAndSessionErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		httpCode int
		body     string
		want     error
	}{
		{name: "expired through ret", httpCode: http.StatusOK, body: `{"ret":-14,"errmsg":"secret-data"}`, want: ErrSessionExpired},
		{name: "expired through errcode", httpCode: http.StatusOK, body: `{"errcode":-14,"errmsg":"secret-data"}`, want: ErrSessionExpired},
		{name: "application rejection", httpCode: http.StatusOK, body: `{"ret":12,"errmsg":"secret-data"}`},
		{name: "HTTP rejection", httpCode: http.StatusBadRequest, body: "secret-data"},
		{name: "HTTP failure", httpCode: http.StatusBadGateway, body: "secret-data", want: ErrSendUncertain},
		{name: "ambiguous response", httpCode: http.StatusOK, body: "secret-data", want: ErrSendUncertain},
		{name: "null response", httpCode: http.StatusOK, body: "null", want: ErrSendUncertain},
		{name: "network failure", want: ErrSendUncertain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
				if tc.httpCode == 0 {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				w.WriteHeader(tc.httpCode)
				fmt.Fprint(w, tc.body)
			})
			err := client.Send(t.Context(), testAccount(), "context", "operation", "text")
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) ||
				(tc.want == nil && (errors.Is(err, ErrSendUncertain) || errors.Is(err, ErrSessionExpired))) ||
				strings.Contains(err.Error(), "secret-data") || strings.Contains(err.Error(), client.testOrigin) {
				t.Errorf("Send yielded %v, want error %v with sensitive fields redacted", err, tc.want)
			}
			if tc.want == ErrSessionExpired {
				_, cursor, err := client.Updates(t.Context(), testAccount(), "previous")
				if !errors.Is(err, ErrSessionExpired) || cursor != "previous" {
					t.Errorf("failed Updates yielded cursor %q and error %v", cursor, err)
				}
			}
		})
	}
}

func TestClientHonorsCanceledPoll(t *testing.T) {
	client := testClient(t, func(_ http.ResponseWriter, _ *http.Request) {
		t.Error("canceled poll reached server")
	})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	scan, err := client.PollQR(ctx, "secret-qr", "secret-pin", "")
	if scan != (Scan{}) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled PollQR = %+v, %v", scan, err)
	}
}

func TestClientKeepsQRPollPendingAfterNetworkFailure(t *testing.T) {
	client := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	scan, err := client.PollQR(t.Context(), "private-qr", "private-pin", "")
	if scan != (Scan{Status: "wait"}) || err != nil {
		t.Fatalf("interrupted PollQR = %+v, %v", scan, err)
	}
}

func testClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := NewClient()
	client.testOrigin = server.URL
	return client
}

func testAccount() Account {
	return Account{Token: "local-token", BotID: "bot", OwnerID: "owner"}
}

func checkAppHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("iLink-App-Id") != "bot" || r.Header.Get("iLink-App-ClientVersion") != "196608" {
		t.Errorf("incorrect client metadata: %v", r.Header)
	}
}

func checkPostHeaders(t *testing.T, r *http.Request, authorization string) {
	t.Helper()
	if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("AuthorizationType") != "ilink_bot_token" || r.Header.Get("Authorization") != authorization {
		t.Errorf("incorrect JSON request headers: %v", r.Header)
	}
	decoded, err := base64.StdEncoding.DecodeString(r.Header.Get("X-WECHAT-UIN"))
	if err != nil {
		t.Errorf("invalid X-WECHAT-UIN: %v", err)
	}
	if _, err := strconv.ParseUint(string(decoded), 10, 32); err != nil {
		t.Errorf("X-WECHAT-UIN is not an encoded uint32: %v", err)
	}
}

func checkBaseInfo(t *testing.T, base map[string]string) {
	t.Helper()
	want := map[string]string{"channel_version": "3.0.0-beta-1", "bot_agent": "Orca-Wails"}
	if !reflect.DeepEqual(base, want) {
		t.Errorf("base_info = %+v, want %+v", base, want)
	}
}

func readRequest(t *testing.T, r *http.Request, body any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		t.Errorf("could not decode request: %v", err)
	}
}

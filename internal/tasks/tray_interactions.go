package tasks

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const structuredTabPrefix = "structured-agent-session-"
const maxAnswerBytes = 64 * 1024
const maxAnswerPayloadBytes = 256 * 1024

var (
	ErrNoQuickQuestion = errors.New("请在原会话中查看并回复")
	ErrQuestionChanged = errors.New("问题已变化，请重新打开后回复")
	ErrAnswerRefused   = errors.New("Core 未接收这条回复，请打开原会话查看")
	ErrAnswerUncertain = errors.New("发送结果待确认，请在原会话查看")
	ErrBotOpen         = errors.New("对话已创建，请从 Orca 的机器人列表打开")
)

func (t Target) StructuredSessionID() string {
	if t.WorktreeID == "" {
		return ""
	}
	id, ok := strings.CutPrefix(t.TabID, structuredTabPrefix)
	if !ok || id == "" || len(id) > 512 {
		return ""
	}
	if t.LeafID != nil {
		// Core publishes a synthetic leaf for structured task rows.
		digest := sha256.Sum256([]byte(id))
		leaf := hex.EncodeToString(digest[:16])
		expected := leaf[:8] + "-" + leaf[8:12] + "-4" + leaf[13:16] + "-a" + leaf[17:20] + "-" + leaf[20:]
		if *t.LeafID != expected {
			return ""
		}
	}
	return id
}

type rpcCall func(method string, params any, output any) error

func withRuntime[T any](ctx context.Context, data string, pid uint32, run func(rpcCall) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	metadata, err := runtimeFor(ctx, data, pid)
	if err != nil {
		var zero T
		return zero, err
	}
	return run(func(method string, params any, output any) error {
		const id = "tray-interaction"
		raw, err := runtimeRequest(ctx, metadata, id, method, params)
		if err != nil {
			return err
		}
		var responseID string
		var ok bool
		var meta responseMeta
		var result json.RawMessage
		if err := decodeObject(raw, required("id", &responseID), required("ok", &ok),
			required("_meta", &meta), defaulted("result", &result)); err != nil {
			return err
		}
		if responseID != id || meta.runtimeID != metadata.runtimeID || !ok || len(result) == 0 {
			return errors.New("Orca Core did not accept the tray request")
		}
		return unmarshalWire(result, output)
	})
}

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"-"`
}

type Question struct {
	ID          string           `json:"id"`
	Revision    uint64           `json:"revision"`
	Text        string           `json:"text"`
	Options     []QuestionOption `json:"options"`
	AcceptsText bool             `json:"acceptsText"`
	AllowEmpty  bool             `json:"allowEmpty"`
	Placeholder string           `json:"placeholder,omitempty"`
	InitialText string           `json:"initialText,omitempty"`
	questionID  string
	fence       uint64
}

type wireQuestion struct {
	ID            string           `json:"id"`
	Text          string           `json:"question"`
	MultiSelect   bool             `json:"multiSelect"`
	Options       []QuestionOption `json:"options"`
	FreeTextID    string           `json:"freeTextQuestionId"`
	FreeTextInput struct {
		AllowEmpty  bool   `json:"allowEmpty"`
		Placeholder string `json:"placeholder"`
		InitialText string `json:"initialValue"`
	} `json:"freeTextInput"`
}

func (q *QuestionOption) UnmarshalJSON(data []byte) error {
	return decodeObject(data, required("id", &q.ID), required("label", &q.Label),
		defaulted("description", &q.Description), defaulted("preview", &q.Preview))
}

func ReadQuestion(ctx context.Context, data string, pid uint32, sessionID string) (*Question, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (*Question, error) {
		return readQuestion(call, sessionID)
	})
}

func readQuestion(call rpcCall, sessionID string) (*Question, error) {
	if sessionID == "" {
		return nil, ErrNoQuickQuestion
	}
	var history struct {
		OK   bool `json:"ok"`
		Page struct {
			SessionID string   `json:"sessionId"`
			Fence     uint64   `json:"fence"`
			Prompts   []string `json:"actionablePromptIds"`
		} `json:"page"`
	}
	if err := call("agentSession.history", map[string]any{
		"sessionId": sessionID, "direction": "tail", "limit": 1,
	}, &history); err != nil {
		return nil, err
	}
	page := history.Page
	if !history.OK || page.SessionID != sessionID || page.Fence == 0 || len(page.Prompts) != 1 || page.Prompts[0] == "" {
		return nil, ErrNoQuickQuestion
	}
	var detail struct {
		Item *struct {
			ID        string `json:"itemId"`
			Revision  uint64 `json:"revision"`
			Shortened bool   `json:"shortened"`
			Body      struct {
				wireQuestion
				Kind       string         `json:"kind"`
				Questions  []wireQuestion `json:"questions"`
				Resolution struct {
					State string `json:"state"`
				} `json:"resolution"`
			} `json:"body"`
		} `json:"item"`
	}
	if err := call("agentSession.item", map[string]string{"sessionId": sessionID, "itemId": page.Prompts[0]}, &detail); err != nil {
		return nil, err
	}
	item := detail.Item
	if item == nil || item.ID != page.Prompts[0] || item.Revision == 0 || item.Shortened ||
		item.Body.Kind != "question" || item.Body.Resolution.State != "pending" {
		return nil, ErrNoQuickQuestion
	}
	wire := item.Body.wireQuestion
	if item.Body.Questions != nil {
		if len(item.Body.Questions) != 1 {
			return nil, ErrNoQuickQuestion
		}
		wire = item.Body.Questions[0]
	} else {
		wire.ID = "q1"
		if wire.FreeTextID != "" {
			wire.ID = wire.FreeTextID
		}
	}
	if wire.ID == "" || wire.MultiSelect || strings.TrimSpace(wire.Text) == "" ||
		(len(wire.Options) == 0 && wire.FreeTextID == "") {
		return nil, ErrNoQuickQuestion
	}
	seen := make(map[string]bool, len(wire.Options))
	for _, option := range wire.Options {
		if option.ID == "" || strings.TrimSpace(option.Label) == "" || option.Preview != "" || seen[option.ID] {
			return nil, ErrNoQuickQuestion
		}
		seen[option.ID] = true
	}
	if wire.Options == nil {
		wire.Options = []QuestionOption{}
	}
	return &Question{ID: item.ID, Revision: item.Revision, Text: wire.Text, Options: wire.Options,
		AcceptsText: wire.FreeTextID != "", AllowEmpty: wire.FreeTextInput.AllowEmpty,
		Placeholder: wire.FreeTextInput.Placeholder, InitialText: wire.FreeTextInput.InitialText,
		questionID: wire.ID, fence: page.Fence}, nil
}

type Answer struct {
	QuestionID string  `json:"id"`
	Revision   uint64  `json:"revision"`
	OptionID   string  `json:"optionId,omitempty"`
	Text       *string `json:"text,omitempty"`
}

// AnswerQuestion revalidates the live prompt and lets Core atomically compare its revision.
func AnswerQuestion(ctx context.Context, data string, pid uint32, sessionID string, answer Answer) error {
	_, err := withRuntime(ctx, data, pid, func(call rpcCall) (struct{}, error) {
		return struct{}{}, answerQuestion(call, sessionID, answer)
	})
	return err
}

func answerQuestion(call rpcCall, sessionID string, answer Answer) error {
	q, err := readQuestion(call, sessionID)
	if err != nil {
		return err
	}
	if q.ID != answer.QuestionID || q.Revision != answer.Revision {
		return ErrQuestionChanged
	}
	fields, err := questionAnswer(q, answer.OptionID, answer.Text)
	if err != nil {
		return err
	}
	encoded, err := coreJSON(fields["answers"])
	if err != nil || len(encoded) > maxAnswerPayloadBytes {
		return errors.New("回复内容超出长度限制")
	}
	fingerprint, err := questionFingerprint(sessionID, fields)
	if err != nil {
		return err
	}

	var entropy [16]byte
	rand.Read(entropy[:])
	fields["envelope"] = map[string]any{
		"sessionId": sessionID, "expectedRuntimeFence": q.fence,
		"clientOperationId":  fmt.Sprintf("%d-%x", time.Now().UnixMilli(), entropy),
		"payloadFingerprint": fingerprint,
	}
	var receipt struct {
		OK      *bool `json:"ok"`
		Refusal struct {
			Code    string `json:"code"`
			Details struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"refusal"`
		Value struct {
			ID         string `json:"itemId"`
			Revision   uint64 `json:"revision"`
			Resolution struct {
				State string `json:"state"`
			} `json:"resolution"`
		} `json:"value"`
	}
	if err := call("agentSession.respondToQuestion", fields, &receipt); err != nil || receipt.OK == nil {
		return ErrAnswerUncertain
	}
	if !*receipt.OK {
		switch receipt.Refusal.Code {
		case "", "agent_session_operation_unknown":
			return ErrAnswerUncertain
		case "agent_session_item_revision_stale", "agent_session_fence_stale":
			return ErrQuestionChanged
		default:
			if receipt.Refusal.Details.Reason == "promptGone" || receipt.Refusal.Details.Reason == "promptResolved" {
				return ErrQuestionChanged
			}
			return ErrAnswerRefused
		}
	}
	if receipt.Value.ID != q.ID || receipt.Value.Revision < q.Revision || receipt.Value.Resolution.State != "resolved" {
		return ErrAnswerUncertain
	}
	return nil
}

func questionAnswer(q *Question, optionID string, text *string) (map[string]any, error) {
	selection := map[string]any{"questionId": q.questionID, "optionIds": []string{}}
	switch {
	case optionID != "" && text == nil:
		for _, option := range q.Options {
			if option.ID == optionID {
				selection["optionIds"] = []string{optionID}
				return map[string]any{"itemId": q.ID, "expectedRevision": q.Revision, "answers": []any{selection}}, nil
			}
		}
	case optionID == "" && text != nil && q.AcceptsText && utf8.ValidString(*text) && len(*text) <= maxAnswerBytes:
		if q.AllowEmpty || strings.TrimSpace(*text) != "" {
			selection["other"] = *text
			return map[string]any{"itemId": q.ID, "expectedRevision": q.Revision, "answers": []any{selection}}, nil
		}
	}
	return nil, errors.New("回复与当前问题不匹配，请检查后重试")
}

func questionFingerprint(sessionID string, fields map[string]any) (string, error) {
	canonical, err := coreJSON(map[string]any{
		"method": "agentSession.respondTo:question", "sessionId": sessionID, "fields": fields,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), nil
}

func coreJSON(value any) (string, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	// Both peers sort keys. Match JSON.stringify for literal Unicode line separators.
	return serdeLineSeparators.Replace(strings.TrimSuffix(buffer.String(), "\n")), nil
}

type Bot struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Specialty *string   `json:"specialty,omitempty"`
	Paused    bool      `json:"paused"`
	Server    *struct{} `json:"server,omitempty"`
}

func ListBots(ctx context.Context, data string, pid uint32) ([]Bot, error) {
	return withRuntime(ctx, data, pid, listBots)
}

func listBots(call rpcCall) ([]Bot, error) {
	var roster struct {
		Bots []Bot `json:"bots"`
	}
	if err := call("bots.roster", map[string]bool{"withoutChats": true}, &roster); err != nil {
		return nil, err
	}
	bots := []Bot{}
	for _, bot := range roster.Bots {
		if bot.ID == "" || bot.Server != nil {
			continue
		}
		bot.Name = firstLabel("未命名 Bot", &bot.Name)
		bot.Specialty = label(bot.Specialty)
		bots = append(bots, bot)
	}
	return bots, nil
}

func StartBotChat(ctx context.Context, data string, pid uint32, botID string) (Target, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (Target, error) {
		bots, err := listBots(call)
		if err != nil {
			return Target{}, err
		}
		for _, bot := range bots {
			if bot.ID == botID && !bot.Paused {
				return startBotChat(call, botID)
			}
		}
		return Target{}, errors.New("这个 Bot 已暂停或不可用")
	})
}

func startBotChat(call rpcCall, botID string) (Target, error) {
	var chat struct {
		SessionID string `json:"sessionId"`
	}
	if err := call("bots.session.start", map[string]string{"botId": botID}, &chat); err != nil || chat.SessionID == "" {
		return Target{}, errors.New("暂时无法新建 Bot 对话")
	}
	var reveal struct {
		OK          bool   `json:"ok"`
		SessionID   string `json:"sessionId"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := call("agentSession.reveal", map[string]string{"sessionId": chat.SessionID}, &reveal); err != nil ||
		!reveal.OK || reveal.WorkspaceID == "" || reveal.SessionID != chat.SessionID {
		return Target{}, ErrBotOpen
	}
	return Target{WorktreeID: reveal.WorkspaceID, TabID: structuredTabPrefix + reveal.SessionID, FocusComposer: true}, nil
}

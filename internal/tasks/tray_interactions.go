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
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

const structuredTabPrefix = "structured-agent-session-"
const maxAnswerBytes = 64 * 1024
const maxAnswerPayloadBytes = 256 * 1024
const maxQuestionParts = 4
const maxSelectedOptions = 64

var (
	ErrNoQuickQuestion = errors.New("请在原会话中查看并回复")
	ErrQuestionChanged = errors.New("问题已变化，请重新打开后回复")
	ErrAnswerRefused   = errors.New("Core 未接收这条回复，请打开原会话查看")
	ErrAnswerUncertain = errors.New("结果待确认，可核对原会话的记录")
	ErrAnswerTooLarge  = errors.New("回复内容超出长度限制，请缩短后发送")
	ErrInvalidAnswer   = errors.New("回复与当前问题不匹配，请检查后重试")
	ErrBotOpen         = errors.New("对话已创建，请从 Orca 的机器人列表打开")
)

func (t Target) StructuredSessionID() string {
	if t.WorktreeID == "" {
		return ""
	}
	id, ok := strings.CutPrefix(t.TabID, structuredTabPrefix)
	if t.ChatSessionID != "" && t.LeafID != nil {
		id, ok = t.ChatSessionID, true
	}
	if !ok || id == "" || len(id) > 512 {
		return ""
	}
	if t.LeafID != nil {
		// Core publishes a synthetic leaf for structured task rows.
		if *t.LeafID != structuredLeafID(id) {
			return ""
		}
	}
	return id
}

func structuredLeafID(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	leaf := hex.EncodeToString(digest[:16])
	return leaf[:8] + "-" + leaf[8:12] + "-4" + leaf[13:16] + "-a" + leaf[17:20] + "-" + leaf[20:]
}

type rpcCall func(method string, params any, output any) error

func withRuntime[T any](ctx context.Context, data string, pid uint32, run func(rpcCall) (T, error)) (T, error) {
	return withRuntimeHost(ctx, data, pid, 8*time.Second, func(call rpcCall, _ string) (T, error) {
		return run(call)
	})
}

func withRuntimeHost[T any](ctx context.Context, data string, pid uint32, timeout time.Duration, run func(rpcCall, string) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
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
	}, metadata.runtimeID)
}

type QuestionOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Preview     string `json:"preview,omitempty"`
	Intent      string `json:"intent,omitempty"`
}

type QuestionPart struct {
	ID          string           `json:"id"`
	Text        string           `json:"text"`
	MultiSelect bool             `json:"multiSelect"`
	Options     []QuestionOption `json:"options"`
	AcceptsText bool             `json:"acceptsText"`
	AllowEmpty  bool             `json:"allowEmpty"`
	Placeholder string           `json:"placeholder,omitempty"`
	InitialText string           `json:"initialText,omitempty"`
}

type Question struct {
	ID       string         `json:"id"`
	Revision uint64         `json:"revision"`
	Parts    []QuestionPart `json:"parts"`
	fence    uint64
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
		defaulted("description", &q.Description), defaulted("preview", &q.Preview), optional("intent", &q.Intent))
}

func readQuestion(call rpcCall, sessionID, requestedID string) (*Question, error) {
	prompt, err := readPrompt(call, sessionID, requestedID, false)
	return prompt.Question, err
}

type promptHistory struct {
	SessionID string   `json:"sessionId"`
	Fence     uint64   `json:"fence"`
	Prompts   []string `json:"actionablePromptIds"`
}

func readPromptHistory(call rpcCall, sessionID string) (promptHistory, error) {
	if sessionID == "" {
		return promptHistory{}, ErrNoQuickQuestion
	}
	var history struct {
		OK   bool          `json:"ok"`
		Page promptHistory `json:"page"`
	}
	if err := call("agentSession.history", map[string]any{
		"sessionId": sessionID, "direction": "tail", "limit": 1,
	}, &history); err != nil {
		return promptHistory{}, err
	}
	page := history.Page
	if !history.OK || page.SessionID != sessionID || page.Fence == 0 {
		return promptHistory{}, ErrNoQuickQuestion
	}
	return page, nil
}

func readPrompt(call rpcCall, sessionID, requestedID string, includeApprovals bool) (Prompt, error) {
	page, err := readPromptHistory(call, sessionID)
	if err != nil {
		return Prompt{}, err
	}
	prompt := Prompt{Count: len(page.Prompts)}
	for _, id := range page.Prompts {
		if id == "" || (requestedID != "" && id != requestedID) {
			continue
		}
		item, err := readPromptItem(call, sessionID, id)
		if err != nil && !errors.Is(err, ErrNoQuickQuestion) {
			return Prompt{}, err
		}
		if item == nil || item.Body.Resolution.State != "pending" {
			continue
		}
		switch item.Body.Kind {
		case "question":
			prompt.Question, _ = item.question(page.Fence)
		case "approval":
			if includeApprovals {
				prompt.Approval = item.approval(page.Fence)
			}
		}
		if prompt.Question != nil || prompt.Approval != nil {
			return prompt, nil
		}
	}
	if requestedID != "" {
		return Prompt{}, ErrQuestionChanged
	}
	return prompt, ErrNoQuickQuestion
}

type promptItem struct {
	ID        string `json:"itemId"`
	Revision  uint64 `json:"revision"`
	Shortened bool   `json:"shortened"`
	Body      struct {
		wireQuestion
		approvalBody
		Kind       string         `json:"kind"`
		Questions  []wireQuestion `json:"questions"`
		Resolution struct {
			State      string              `json:"state"`
			OptionID   string              `json:"selectedOptionId"`
			Answers    []QuestionSelection `json:"answers"`
			ResolvedAt int64               `json:"resolvedAt"`
		} `json:"resolution"`
	} `json:"body"`
}

func readPromptItem(call rpcCall, sessionID, id string) (*promptItem, error) {
	var detail struct {
		Item *promptItem `json:"item"`
	}
	if err := call("agentSession.item", map[string]string{"sessionId": sessionID, "itemId": id}, &detail); err != nil {
		return nil, err
	}
	item := detail.Item
	if item == nil || item.ID != id || item.Revision == 0 || item.Shortened {
		return nil, ErrNoQuickQuestion
	}
	return item, nil
}

func (item *promptItem) question(fence uint64) (*Question, error) {
	wires := item.Body.Questions
	if wires == nil {
		wire := item.Body.wireQuestion
		wire.ID = "q1"
		if wire.FreeTextID != "" {
			wire.ID = wire.FreeTextID
		}
		wires = []wireQuestion{wire}
	}
	if len(wires) == 0 || len(wires) > maxQuestionParts {
		return nil, ErrNoQuickQuestion
	}
	q := &Question{ID: item.ID, Revision: item.Revision, fence: fence}
	seen := make(map[string]bool, len(wires))
	for _, wire := range wires {
		if wire.ID == "" || seen[wire.ID] || strings.TrimSpace(wire.Text) == "" ||
			(len(wire.Options) == 0 && wire.FreeTextID == "") {
			return nil, ErrNoQuickQuestion
		}
		seen[wire.ID] = true
		options := make(map[string]bool, len(wire.Options))
		for _, option := range wire.Options {
			if option.ID == "" || strings.TrimSpace(option.Label) == "" || options[option.ID] {
				return nil, ErrNoQuickQuestion
			}
			options[option.ID] = true
		}
		if wire.Options == nil {
			wire.Options = []QuestionOption{}
		}
		q.Parts = append(q.Parts, QuestionPart{
			ID: wire.ID, Text: wire.Text, MultiSelect: wire.MultiSelect, Options: wire.Options,
			AcceptsText: wire.FreeTextID != "", AllowEmpty: wire.FreeTextInput.AllowEmpty,
			Placeholder: wire.FreeTextInput.Placeholder, InitialText: wire.FreeTextInput.InitialText,
		})
	}
	return q, nil
}

type QuestionSelection struct {
	QuestionID string   `json:"questionId"`
	OptionIDs  []string `json:"optionIds"`
	Other      *string  `json:"other,omitempty"`
}

type Answer struct {
	QuestionID     string              `json:"id"`
	Revision       uint64              `json:"revision"`
	Selections     []QuestionSelection `json:"selections"`
	ApprovalOption string              `json:"approvalOption,omitempty"`
}

// AnswerQuestion revalidates the live prompt and lets Core atomically compare its revision.
func AnswerQuestion(ctx context.Context, data string, pid uint32, sessionID string, answer Answer) error {
	_, err := withRuntime(ctx, data, pid, func(call rpcCall) (struct{}, error) {
		return struct{}{}, answerQuestion(call, sessionID, answer)
	})
	return err
}

func answerQuestion(call rpcCall, sessionID string, answer Answer) error {
	if answer.QuestionID == "" {
		return ErrQuestionChanged
	}
	prompt, err := readPrompt(call, sessionID, answer.QuestionID, answer.ApprovalOption != "")
	if err != nil {
		return err
	}
	if answer.ApprovalOption != "" {
		approval := prompt.Approval
		if approval == nil || approval.Revision != answer.Revision ||
			!slices.ContainsFunc(approval.Options, func(option QuestionOption) bool { return option.ID == answer.ApprovalOption }) {
			return ErrInvalidAnswer
		}
		return submitPrompt(call, sessionID, "Approval", approval.ID, approval.Revision, approval.fence, map[string]any{
			"itemId": approval.ID, "expectedRevision": approval.Revision, "optionId": answer.ApprovalOption,
		})
	}
	q := prompt.Question
	if q.ID != answer.QuestionID || q.Revision != answer.Revision {
		return ErrQuestionChanged
	}
	fields, err := questionAnswer(q, answer.Selections)
	if err != nil {
		return err
	}
	encoded, err := coreJSON(fields["answers"])
	if err != nil || len(encoded) > maxAnswerPayloadBytes {
		return ErrAnswerTooLarge
	}
	return submitPrompt(call, sessionID, "Question", q.ID, q.Revision, q.fence, fields)
}

func submitPrompt(call rpcCall, sessionID, kind, itemID string, revision, fence uint64, fields map[string]any) error {
	envelope, err := mutationEnvelope("agentSession.respondTo:"+strings.ToLower(kind), sessionID, fence, newOperationID(), fields)
	if err != nil {
		return err
	}
	fields["envelope"] = envelope
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
	if err := call("agentSession.respondTo"+kind, fields, &receipt); err != nil || receipt.OK == nil {
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
	if receipt.Value.ID != itemID || receipt.Value.Revision < revision || receipt.Value.Resolution.State != "resolved" {
		return ErrAnswerUncertain
	}
	return nil
}

func questionAnswer(q *Question, selections []QuestionSelection) (map[string]any, error) {
	if len(selections) != len(q.Parts) {
		return nil, ErrInvalidAnswer
	}
	answers := make([]map[string]any, 0, len(q.Parts))
	for _, part := range q.Parts {
		index := slices.IndexFunc(selections, func(selection QuestionSelection) bool {
			return selection.QuestionID == part.ID
		})
		if index < 0 {
			return nil, ErrInvalidAnswer
		}
		selected := selections[index]
		optionIDs := selected.OptionIDs
		if optionIDs == nil {
			optionIDs = []string{}
		}
		if len(optionIDs) > maxSelectedOptions {
			return nil, ErrInvalidAnswer
		}
		for i, id := range optionIDs {
			if slices.Contains(optionIDs[:i], id) || !slices.ContainsFunc(part.Options, func(option QuestionOption) bool {
				return option.ID == id
			}) {
				return nil, ErrInvalidAnswer
			}
		}
		count := len(optionIDs)
		answer := map[string]any{"questionId": part.ID, "optionIds": optionIDs}
		if text := selected.Other; text != nil {
			if len(*text) > maxAnswerBytes {
				return nil, ErrAnswerTooLarge
			}
			if !part.AcceptsText || !utf8.ValidString(*text) ||
				(!part.AllowEmpty && strings.TrimSpace(*text) == "") {
				return nil, ErrInvalidAnswer
			}
			answer["other"] = *text
			count++
		}
		if count == 0 || (!part.MultiSelect && count != 1) {
			return nil, ErrInvalidAnswer
		}
		answers = append(answers, answer)
	}
	return map[string]any{"itemId": q.ID, "expectedRevision": q.Revision, "answers": answers}, nil
}

func questionFingerprint(sessionID string, fields map[string]any) (string, error) {
	return payloadFingerprint("agentSession.respondTo:question", sessionID, fields)
}

func newOperationID() string {
	var entropy [16]byte
	rand.Read(entropy[:])
	return fmt.Sprintf("%d-%x", time.Now().UnixMilli(), entropy)
}

func mutationEnvelope(method, sessionID string, fence uint64, operationID string, fields map[string]any) (map[string]any, error) {
	fingerprint, err := payloadFingerprint(method, sessionID, fields)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"sessionId": sessionID, "expectedRuntimeFence": fence,
		"clientOperationId": operationID, "payloadFingerprint": fingerprint,
	}, nil
}

func payloadFingerprint(method, sessionID string, fields map[string]any) (string, error) {
	canonical, err := coreJSON(map[string]any{
		"method": method, "sessionId": sessionID, "fields": fields,
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

package tasks

import (
	"context"
	"slices"
	"strings"
)

type Prompt struct {
	Question *Question `json:"question,omitempty"`
	Approval *Approval `json:"approval,omitempty"`
	Count    int       `json:"count"`
}

type Approval struct {
	ID       string           `json:"id"`
	Revision uint64           `json:"revision"`
	Options  []QuestionOption `json:"options"`
	approvalBody
	fence uint64
}

type approvalBody struct {
	Title          string `json:"title"`
	ToolName       string `json:"toolName"`
	Detail         string `json:"detail"`
	Description    string `json:"description"`
	DecisionReason string `json:"decisionReason"`
	BlockedPath    string `json:"blockedPath"`
	Subject        *struct {
		Kind     string `json:"kind"`
		Text     string `json:"text"`
		FilePath string `json:"filePath"`
	} `json:"subject,omitempty"`
	MatchedAskRule *struct {
		Source      string `json:"source"`
		ToolName    string `json:"toolName"`
		RuleContent string `json:"ruleContent"`
	} `json:"matchedAskRule,omitempty"`
}

func (item *promptItem) approval(fence uint64) *Approval {
	body := item.Body.approvalBody
	if strings.TrimSpace(body.Title) == "" {
		return nil
	}
	if body.Subject != nil {
		if body.Subject.Kind != "plan" || !completeApprovalText(body.Subject.Text) {
			return nil
		}
		// Core may shorten the duplicate detail even while retaining the full plan.
		body.Detail = ""
	} else {
		scope := body.Detail
		if strings.TrimSpace(scope) == "" {
			scope = body.BlockedPath
		}
		if !completeApprovalText(scope) {
			return nil
		}
	}
	options := make([]QuestionOption, 0, 2)
	seen := make(map[string]bool, len(item.Body.Options))
	for _, option := range item.Body.Options {
		if option.ID == "" || seen[option.ID] {
			return nil
		}
		seen[option.ID] = true
		if option.Intent == "allow_once" || option.Intent == "reject_once" {
			options = append(options, option)
		}
	}
	if len(options) == 0 {
		return nil
	}
	return &Approval{ID: item.ID, Revision: item.Revision, Options: options, approvalBody: body, fence: fence}
}

func completeApprovalText(text string) bool {
	text = strings.TrimSpace(text)
	return text != "" && !strings.HasSuffix(text, "…") && !strings.Contains(text, "[Orca: output truncated")
}

func ReadPrompt(ctx context.Context, data string, pid uint32, sessionID string) (Prompt, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (Prompt, error) {
		return readPrompt(call, sessionID, "", true)
	})
}

type AnswerRecord int

const (
	AnswerUnconfirmed AnswerRecord = iota
	AnswerRecorded
	PromptAlreadySettled
)

func CheckAnswer(ctx context.Context, data string, pid uint32, sessionID string, answer Answer, since int64) (AnswerRecord, error) {
	return withRuntime(ctx, data, pid, func(call rpcCall) (AnswerRecord, error) {
		return checkAnswer(call, sessionID, answer, since)
	})
}

func checkAnswer(call rpcCall, sessionID string, answer Answer, since int64) (AnswerRecord, error) {
	item, err := readPromptItem(call, sessionID, answer.QuestionID)
	if err != nil {
		return AnswerUnconfirmed, err
	}
	resolution := item.Body.Resolution
	if item.Revision <= answer.Revision || (resolution.State != "resolved" && resolution.State != "cancelled") {
		return AnswerUnconfirmed, nil
	}
	if resolution.State == "resolved" && since > 0 && resolution.ResolvedAt >= since && item.matchesAnswer(answer) {
		return AnswerRecorded, nil
	}
	return PromptAlreadySettled, nil
}

func (item *promptItem) matchesAnswer(answer Answer) bool {
	resolution := item.Body.Resolution
	if answer.ApprovalOption != "" {
		return item.Body.Kind == "approval" && resolution.OptionID == answer.ApprovalOption
	}
	if item.Body.Kind != "question" || len(resolution.Answers) == 0 || len(resolution.Answers) != len(answer.Selections) {
		return false
	}
	for _, expected := range answer.Selections {
		i := slices.IndexFunc(resolution.Answers, func(got QuestionSelection) bool { return got.QuestionID == expected.QuestionID })
		if i < 0 {
			return false
		}
		got := resolution.Answers[i]
		choices := slices.Clone(got.OptionIDs)
		want := slices.Clone(expected.OptionIDs)
		slices.Sort(choices)
		slices.Sort(want)
		if !slices.Equal(choices, want) || (got.Other == nil) != (expected.Other == nil) ||
			(got.Other != nil && *got.Other != *expected.Other) {
			return false
		}
	}
	return true
}

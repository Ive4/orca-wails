package wechat

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"orca-wails/internal/tasks"
)

const (
	maxTaskMessageBytes  = 4000
	maxQuestionBodyBytes = maxTaskMessageBytes - 540 // Reserve room for task metadata, the detail command, and a reply error.
	maxTaskReplyBytes    = 8192
	textReplyPrefix      = "文字"
)

var ErrReplyInvalid = errors.New("回复与当前问题不匹配，请按提示重试")

type Task struct {
	Code string
	Row  tasks.Row
}

type ReplyDraft struct {
	Code       string
	Question   tasks.Question
	Selections []tasks.QuestionSelection
}

func TaskList(rows []Task) string {
	if len(rows) == 0 {
		return "暂无任务。"
	}
	rows = slices.Clone(rows)
	slices.SortStableFunc(rows, func(a, b Task) int {
		return cmp.Compare(taskListGroup(a.Row.Status), taskListGroup(b.Row.Status))
	})
	command := fmt.Sprintf("\n\n发送「任务 %s」查看详情。", rows[0].Code)
	var list strings.Builder
	list.WriteString("当前任务")
	for i, task := range rows {
		line := "\n\n" + taskHeader(task)
		omission := fmt.Sprintf("\n\n另有 %d 项任务。", len(rows)-i)
		if list.Len()+len(line)+len(command)+len(omission) > maxTaskMessageBytes {
			list.WriteString(omission)
			break
		}
		list.WriteString(line)
	}
	list.WriteString(command)
	return list.String()
}

func TaskDetails(task Task, summary string) string {
	return taskMessage(task, taskText(summary, 900))
}

func Completion(task Task, summary string) string {
	result := taskText(summary, 900)
	if result == "" {
		result = "暂无结果摘要。"
	}
	return taskMessage(task, result)
}

func taskMessage(task Task, text string) string {
	if text == "" {
		return taskHeader(task)
	}
	return taskHeader(task) + "\n\n" + text
}

func taskHeader(task Task) string {
	title := taskText(task.Row.Title, 120)
	if title == "" {
		title = "未命名任务"
	}
	workspace := taskText(task.Row.Workspace, 120)
	if workspace == "" {
		workspace = "工作区"
	}
	updated := "更新时间未知"
	if ms := task.Row.UpdatedAt; ms != nil && *ms > 0 && *ms <= math.MaxInt64 {
		updated = "更新于 " + time.UnixMilli(int64(*ms)).Format("01-02 15:04")
	}
	return fmt.Sprintf("%s · %s · %s\n工作区「%s」 · %s", task.Code, title, taskStatus(task.Row), workspace, updated)
}

func taskListGroup(status tasks.Status) int {
	switch status {
	case tasks.Waiting, tasks.Blocked:
		return 0
	case tasks.Done:
		return 2
	default:
		return 1
	}
}

func taskStatus(row tasks.Row) string {
	switch row.Status {
	case tasks.Waiting:
		return "等待输入"
	case tasks.Blocked:
		return "受阻"
	case tasks.Working:
		return "运行中"
	case tasks.Monitoring:
		return "监测中"
	case tasks.Done:
		switch row.Outcome {
		case "success":
			return "已完成"
		case "failure":
			return "失败"
		case "cancellation":
			return "已取消"
		case "superseded":
			return "已被替代"
		case "interruption":
			return "已中断"
		case "unconfirmed":
			return "结果待确认"
		default:
			return "已结束"
		}
	default:
		return "状态待确认"
	}
}

func QuestionText(task Task, draft ReplyDraft) string {
	_, instructions := replyInstructions(draft)
	if instructions == "" {
		instructions = "请在 Orca 原会话中查看并回复。"
	}
	return taskMessage(task, instructions) + fmt.Sprintf("\n\n发送「任务 %s」查看详情。", task.Code)
}

func replyInstructions(draft ReplyDraft) (tasks.QuestionPart, string) {
	index := len(draft.Selections)
	if draft.Question.ID == "" || draft.Question.Revision == 0 || index >= len(draft.Question.Parts) {
		return tasks.QuestionPart{}, ""
	}
	part := draft.Question.Parts[index]
	question := taskText(part.Text, maxTaskMessageBytes)
	if question == "" || (len(part.Options) == 0 && !part.AcceptsText) {
		return tasks.QuestionPart{}, ""
	}
	var message strings.Builder
	command := draft.Code + " "
	if total := len(draft.Question.Parts); total > 1 {
		fmt.Fprintf(&message, "问题 %d/%d\n", index+1, total)
		command += fmt.Sprintf("%d: ", index+1)
	}
	message.WriteString(question)
	for i, option := range part.Options {
		label := taskText(option.Label, maxTaskMessageBytes)
		if label == "" {
			return tasks.QuestionPart{}, ""
		}
		fmt.Fprintf(&message, "\n%d. %s", i+1, label)
		if description := taskText(option.Description, maxTaskMessageBytes); description != "" {
			fmt.Fprintf(&message, "（%s）", description)
		}
		if message.Len() > maxQuestionBodyBytes {
			return tasks.QuestionPart{}, ""
		}
	}
	if len(part.Options) > 0 {
		fmt.Fprintf(&message, "\n\n回复「%s1」选择第 1 项。", command)
		if part.MultiSelect && len(part.Options) > 1 {
			fmt.Fprintf(&message, "\n多选可回复「%s1,2」。", command)
		}
	}
	if part.AcceptsText {
		fmt.Fprintf(&message, "\n\n回复「%s%s 你的答案」填写文字。", command, textReplyPrefix)
		if part.AllowEmpty {
			fmt.Fprintf(&message, "\n回复「%s%s」提交空白文字。", command, textReplyPrefix)
		}
		if part.MultiSelect && len(part.Options) > 0 {
			fmt.Fprintf(&message, "\n回复「%s1 %s 你的答案」同时选择和填写。", command, textReplyPrefix)
		}
	}
	if message.Len() > maxQuestionBodyBytes {
		return tasks.QuestionPart{}, ""
	}
	return part, message.String()
}

// ParseReply receives the payload after the controller has matched its reply code.
// A group reply begins with the current 1-based part number and a colon.
// Use “文字” before custom text and comma-separated numbers for selections.
func ParseReply(draft ReplyDraft, text string) (ReplyDraft, *tasks.Answer, error) {
	if len(text) > maxTaskReplyBytes || !utf8.ValidString(text) {
		return draft, nil, ErrReplyInvalid
	}
	part, instructions := replyInstructions(draft)
	if instructions == "" {
		return draft, nil, ErrReplyInvalid
	}
	text = strings.TrimSpace(text)
	if len(draft.Question.Parts) > 1 {
		index := strconv.Itoa(len(draft.Selections) + 1)
		payload, ok := strings.CutPrefix(text, index+":")
		if !ok {
			payload, ok = strings.CutPrefix(text, index+"：")
		}
		if !ok {
			return draft, nil, ErrReplyInvalid
		}
		text = strings.TrimSpace(payload)
	}
	selection, ok := readSelection(part, text)
	if !ok {
		return draft, nil, ErrReplyInvalid
	}
	draft.Selections = append(slices.Clone(draft.Selections), selection)
	if len(draft.Selections) < len(draft.Question.Parts) {
		return draft, nil, nil
	}
	return draft, &tasks.Answer{
		QuestionID: draft.Question.ID,
		Revision:   draft.Question.Revision,
		Selections: draft.Selections,
	}, nil
}

func readSelection(part tasks.QuestionPart, text string) (tasks.QuestionSelection, bool) {
	selection := tasks.QuestionSelection{QuestionID: part.ID}
	choices, other, hasText := strings.Cut(text, textReplyPrefix)
	count := 0
	if hasText {
		trimmedChoices := strings.TrimRightFunc(choices, unicode.IsSpace)
		trimmedText := strings.TrimLeftFunc(other, unicode.IsSpace)
		if !part.AcceptsText || (choices != "" && choices == trimmedChoices) ||
			(other != "" && other == trimmedText) || (trimmedText == "" && !part.AllowEmpty) {
			return selection, false
		}
		choices, selection.Other, count = trimmedChoices, &trimmedText, 1
	}
	if choices != "" {
		for raw := range strings.SplitSeq(strings.ReplaceAll(choices, "，", ","), ",") {
			number := strings.TrimSpace(raw)
			index, err := strconv.Atoi(number)
			if err != nil || number[0] < '1' || number[0] > '9' || index < 1 || index > len(part.Options) || len(selection.OptionIDs) >= 64 {
				return selection, false
			}
			id := part.Options[index-1].ID
			if slices.Contains(selection.OptionIDs, id) {
				return selection, false
			}
			selection.OptionIDs = append(selection.OptionIDs, id)
			count++
		}
	}
	return selection, count == 1 || (part.MultiSelect && count > 0)
}

func taskText(value string, limit int) string {
	// Keep Orca pairing offers and authentication values on the local device.
	decoded := strings.ToLower(value)
	for range 4 {
		if strings.Contains(decoded, "orca://") || strings.Contains(decoded, "orca-pair:") ||
			strings.Contains(decoded, "orcapair:") || strings.Contains(decoded, "pairing=") ||
			strings.Contains(decoded, "bearertoken") || strings.Contains(decoded, "authorization: bearer ") {
			return ""
		}
		next, err := url.PathUnescape(decoded)
		if err != nil || next == decoded {
			break
		}
		decoded = strings.ToLower(next)
	}
	value = strings.Join(strings.FieldsFunc(value, func(ch rune) bool {
		return unicode.IsSpace(ch) || unicode.IsControl(ch)
	}), " ")
	if len(value) <= limit {
		return strings.ToValidUTF8(value, "")
	}
	end := limit - len("…")
	for !utf8.RuneStart(value[end]) {
		end--
	}
	return strings.ToValidUTF8(strings.TrimRightFunc(value[:end], unicode.IsSpace), "") + "…"
}

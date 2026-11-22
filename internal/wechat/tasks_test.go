package wechat

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"orca-wails/internal/tasks"
)

func TestParseQuestionReply(t *testing.T) {
	part := tasks.QuestionPart{
		ID: "q1", Text: "选择测试方式", MultiSelect: true, AcceptsText: true,
		Options: []tasks.QuestionOption{{ID: "simulated", Label: "模拟账号"}, {ID: "manual", Label: "手动验证"}, {ID: "new", Label: "新账号"}},
	}
	draft := ReplyDraft{Code: "A17-2", Question: tasks.Question{ID: "question", Revision: 8, Parts: []tasks.QuestionPart{part}}}
	for _, tc := range []struct {
		text  string
		ids   []string
		other string
	}{
		{text: "1", ids: []string{"simulated"}},
		{text: "1， 3", ids: []string{"simulated", "new"}},
		{text: "文字 手动检查", other: "手动检查"},
		{text: "1,3 文字 手动检查", ids: []string{"simulated", "new"}, other: "手动检查"},
	} {
		t.Run(tc.text, func(t *testing.T) {
			next, answer, err := ParseReply(draft, tc.text)
			if err != nil || answer == nil || answer.QuestionID != "question" || answer.Revision != 8 || len(answer.Selections) != 1 || len(next.Selections) != 1 {
				t.Fatalf("ParseReply(%q) = %+v, %+v, %v", tc.text, next, answer, err)
			}
			got := answer.Selections[0]
			if got.QuestionID != "q1" || !slices.Equal(got.OptionIDs, tc.ids) || (tc.other == "" && got.Other != nil) || (tc.other != "" && (got.Other == nil || *got.Other != tc.other)) {
				t.Fatalf("selection = %+v", got)
			}
		})
	}
	for _, text := range []string{"", "0", "+1", "4", "1,", "1,1", "1 3", "文字", "A17-2 1", "消息", "文字 " + strings.Repeat("字", 22000), "文字 \xff"} {
		if next, answer, err := ParseReply(draft, text); !errors.Is(err, ErrReplyInvalid) || answer != nil || len(next.Selections) != 0 {
			t.Errorf("invalid reply %q was accepted or changed its draft", []rune(text)[:min(len([]rune(text)), 20)])
		}
	}
}

func TestParseGroupedReplyRejectsDelayedPart(t *testing.T) {
	part1 := tasks.QuestionPart{ID: "q1", Text: "是否继续？", Options: []tasks.QuestionOption{{ID: "go", Label: "继续"}, {ID: "pause", Label: "暂停"}}}
	part2 := tasks.QuestionPart{ID: "q2", Text: "提供账号名", AcceptsText: true, AllowEmpty: true}
	first := ReplyDraft{Code: "A17-2", Question: tasks.Question{ID: "group", Revision: 3, Parts: []tasks.QuestionPart{part1, part2}}}
	task := Task{Code: "A17", Row: tasks.Row{Title: "前端 Bot", Workspace: "登录页", Status: tasks.Waiting}}
	if prompt := QuestionText(task, first); !strings.Contains(prompt, "A17-2 1: 1") || strings.Contains(prompt, "提供账号名") {
		t.Fatalf("first prompt = %q", prompt)
	}
	for _, text := range []string{"1", "1: 1,2", "1: 文字 继续", "2: 文字"} {
		if _, answer, err := ParseReply(first, text); !errors.Is(err, ErrReplyInvalid) || answer != nil {
			t.Errorf("invalid reply %q was accepted", text)
		}
	}
	second, answer, err := ParseReply(first, "1: 1")
	if err != nil || answer != nil || len(second.Selections) != 1 || len(first.Selections) != 0 {
		t.Fatalf("first reply = %+v, %+v, %v", second, answer, err)
	}
	if prompt := QuestionText(task, second); !strings.Contains(prompt, "A17-2 2: 文字") || strings.Contains(prompt, "是否继续") {
		t.Fatalf("second prompt = %q", prompt)
	}
	for _, text := range []string{"1: 1", "文字 空", "2: 1"} {
		if next, answer, err := ParseReply(second, text); !errors.Is(err, ErrReplyInvalid) || answer != nil || len(next.Selections) != 1 {
			t.Errorf("invalid reply %q was accepted or changed its draft", text)
		}
	}
	_, answer, err = ParseReply(second, "2：文字")
	if err != nil || answer == nil || answer.QuestionID != "group" || answer.Revision != 3 || len(answer.Selections) != 2 {
		t.Fatalf("second reply = %+v, %v", answer, err)
	}
	if a, b := answer.Selections[0], answer.Selections[1]; a.QuestionID != "q1" || !slices.Equal(a.OptionIDs, []string{"go"}) || b.QuestionID != "q2" || b.Other == nil || *b.Other != "" {
		t.Fatalf("group answers = %+v", answer.Selections)
	}
}

func TestTaskListOrdersTasksAndPreservesDetailsCommand(t *testing.T) {
	updated := uint64(time.Date(2026, 11, 22, 9, 30, 0, 0, time.Local).UnixMilli())
	rows := []Task{
		{Code: "A3", Row: tasks.Row{Title: "测试 Bot", Workspace: "发布", Status: tasks.Working}},
		{Code: "A17", Row: tasks.Row{Title: "前端 Bot", Workspace: "登录页", Status: tasks.Waiting, UpdatedAt: &updated}},
		{Code: "A18", Row: tasks.Row{Title: "后端 Bot", Workspace: "登录页", Status: tasks.Blocked}},
	}
	list := TaskList(rows)
	if waiting, blocked, running := strings.Index(list, "A17 ·"), strings.Index(list, "A18 ·"), strings.Index(list, "A3 ·"); waiting < 0 || blocked < 0 || running < 0 || waiting >= blocked || blocked >= running || rows[0].Code != "A3" {
		t.Fatalf("list order or input slice changed: %q, %+v", list, rows)
	}
	for _, want := range []string{"登录页", "等待输入", "受阻", "运行中", "11-22 09:30", "任务 A17"} {
		if !strings.Contains(list, want) {
			t.Errorf("list missing %q: %q", want, list)
		}
	}
	for range 200 {
		rows = append(rows, Task{Code: "A99", Row: tasks.Row{Title: strings.Repeat("项", 4000), Workspace: strings.Repeat("目", 4000), Status: tasks.Done}})
	}
	list = TaskList(rows)
	if len(list) > 4000 || !utf8.ValidString(list) || !strings.Contains(list, "任务 A17") || !strings.Contains(list, "另有") {
		t.Fatalf("oversize list has %d bytes and missing guidance: %q", len(list), list[len(list)-min(len(list), 150):])
	}
}

func TestTaskDetailsUsesOnlyProvidedSummaryAndCoreOutcome(t *testing.T) {
	private := "complete private transcript"
	task := Task{Code: "A17", Row: tasks.Row{
		Title: "前端 Bot", Workspace: "登录页", Status: tasks.Done, ID: "task debug id", ConversationID: "conversation debug id", LastResult: &private,
	}}
	for _, tc := range []struct{ outcome, label string }{
		{"success", "已完成"}, {"failure", "失败"}, {"cancellation", "已取消"}, {"interruption", "已中断"}, {"superseded", "已被替代"}, {"unconfirmed", "结果待确认"}, {"", "已结束"},
	} {
		task.Row.Outcome = tc.outcome
		for _, got := range []string{TaskDetails(task, "完成渲染测试"), Completion(task, "完成渲染测试")} {
			for _, want := range []string{"A17", "前端 Bot", "登录页", tc.label, "完成渲染测试"} {
				if !strings.Contains(got, want) {
					t.Errorf("message missing %q: %q", want, got)
				}
			}
			if strings.Contains(got, "debug id") || strings.Contains(got, private) {
				t.Errorf("message exposed internal data: %q", got)
			}
		}
	}
	for _, supplied := range []string{"orca://connect?pairing=private", "ORCA-PAIR:private", "\x00" + strings.Repeat("结果", 4500)} {
		got := Completion(task, supplied)
		if len(got) > 4000 || strings.Contains(got, "private") || strings.ContainsRune(got, '\x00') || !utf8.ValidString(got) {
			t.Errorf("oversize or private result travelled to WeChat: %q", got)
		}
	}
}

func TestQuestionTextIncludesOnlyUserFacingContentAndUsableCommands(t *testing.T) {
	task := Task{Code: "A17", Row: tasks.Row{Title: "前端 Bot", Workspace: "登录页", Status: tasks.Waiting}}
	draft := ReplyDraft{Code: "A17-2", Question: tasks.Question{ID: "prompt-debug-id", Revision: 12, Parts: []tasks.QuestionPart{{
		ID: "part-debug-id", Text: "选择登录方式。", AcceptsText: true, MultiSelect: true, InitialText: "private initial value",
		Options: []tasks.QuestionOption{{ID: "first-debug-id", Label: "模拟账号", Description: "用于本地验证", Preview: "private preview"}, {ID: "second-debug-id", Label: "测试账号"}},
	}}}}
	message := QuestionText(task, draft)
	for _, want := range []string{"选择登录方式。", "1. 模拟账号", "2. 测试账号", "用于本地验证", "A17-2 1", "A17-2 1,2", "A17-2 文字", "任务 A17"} {
		if !strings.Contains(message, want) {
			t.Errorf("prompt missing %q: %q", want, message)
		}
	}
	if strings.Contains(message, "debug-id") || strings.Contains(message, "private") {
		t.Fatalf("prompt exposed internal data: %q", message)
	}
	label := strings.Repeat("需要人工确认的选项", 7) + "，完成回滚后才能继续"
	draft.Question.Parts[0].Options[0].Label = label
	if msg := QuestionText(task, draft); !strings.Contains(msg, label) {
		t.Errorf("reply command omitted part of the choice: %q", msg)
	}
	for i := range 1000 {
		draft.Question.Parts[0].Options = append(draft.Question.Parts[0].Options, tasks.QuestionOption{ID: string(rune(i + 1)), Label: strings.Repeat("选项", 1000)})
	}
	if msg := QuestionText(task, draft); len(msg) > 4000 || strings.Contains(msg, "A17-2 1") || !strings.Contains(msg, "Orca 原会话") {
		t.Errorf("oversize question displayed unsafe or missing guidance: %q", msg)
	}
	if _, answer, err := ParseReply(draft, "1"); !errors.Is(err, ErrReplyInvalid) || answer != nil {
		t.Error("parsed an option for a question that must be answered in Orca")
	}
}

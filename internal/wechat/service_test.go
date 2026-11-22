package wechat

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"orca-wails/internal/tasks"
)

func TestWeChatOwnerCanResumeAndReceiveCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := uint64(time.Now().Add(time.Minute).UnixMilli())
	core := &fixtureCore{
		row:      tasks.Row{ID: "session", Title: "Frontend Bot", Workspace: "Login", Status: tasks.Waiting, Local: true, QuickReply: true},
		question: tasks.Question{ID: "q", Revision: 1, Parts: []tasks.QuestionPart{{ID: "choice", Text: "Which account?", Options: []tasks.QuestionOption{{ID: "mock", Label: "Mock account"}}}}},
	}
	transport := &fixtureTransport{dns: make(chan Inbound, 16), sent: make(chan string, 16)}
	path := filepath.Join(t.TempDir(), "wechat.json")
	service := Open(ctx, path, transport, core.api(), func() {})
	service.StartPair()
	status := waitPhase(t, service, "verify")
	transport.dns <- Inbound{ID: "unauthorized", From: "someone", To: "bot", Text: status.Code, ContextToken: "bad-context"}
	transport.dns <- Inbound{ID: "bind", From: "owner", To: "bot", Text: status.Code, ContextToken: "context"}
	waitPhase(t, service, "connected")
	if reply := nextText(t, transport.sent); !strings.Contains(reply, "已绑定") {
		t.Fatalf("unexpected binding reply: %q", reply)
	}
	service.Refresh()
	if notice := nextText(t, transport.sent); !strings.Contains(notice, "A1-1 1") || !strings.Contains(notice, "Which account?") {
		t.Fatalf("unexpected blocker: %q", notice)
	}
	transport.dns <- Inbound{ID: "answer", From: "owner", To: "bot", Text: "A1-1 1", ContextToken: "context"}
	if reply := nextText(t, transport.sent); !strings.Contains(reply, "已提交") {
		t.Fatalf("unexpected answer receipt: %q", reply)
	}
	core.mu.Lock()
	result := "All tests passed."
	core.row.Status, core.row.StartedAt, core.row.LastResult = tasks.Done, &now, &result
	core.row.Outcome = "success"
	core.mu.Unlock()
	service.Refresh()
	if notice := nextText(t, transport.sent); !strings.Contains(notice, "已完成") || !strings.Contains(notice, result) {
		t.Fatalf("unexpected completion: %q", notice)
	}
	service.Refresh()
	transport.dns <- Inbound{ID: "old-reply", From: "owner", To: "bot", Text: "A1-1 1", ContextToken: "context"}
	if reply := nextText(t, transport.sent); !strings.Contains(reply, "过期") {
		t.Fatalf("old reply had side effects: %q", reply)
	}
	if err := service.Disconnect(); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, service, "unbound")
	core.mu.Lock()
	core.row.Status, core.row.StartedAt, core.row.LastResult = tasks.Waiting, nil, nil
	core.mu.Unlock()
	service = Open(ctx, path, transport, core.api(), func() {})
	service.StartPair()
	status = waitPhase(t, service, "verify")
	transport.dns <- Inbound{ID: "rebind", From: "owner", To: "bot", Text: status.Code, ContextToken: "context"}
	waitPhase(t, service, "connected")
	nextText(t, transport.sent)
	service.Refresh()
	if notice := nextText(t, transport.sent); !strings.Contains(notice, "A2.1-1 1") {
		t.Fatalf("expected a newly scoped reply code: %q", notice)
	}
	transport.dns <- Inbound{ID: "old-binding-reply", From: "owner", To: "bot", Text: "A1-1 1", ContextToken: "context"}
	if reply := nextText(t, transport.sent); !strings.Contains(reply, "过期") {
		t.Fatalf("the old binding could affect a new task: %q", reply)
	}
}

func TestCompletionObservedOfflineSurvivesRestartWithoutAWorktreeRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "wechat.json")
	transport := &fixtureTransport{dns: make(chan Inbound, 1), sent: make(chan string, 4)}
	result := "Completed during the outage."
	now := uint64(time.Now().UnixMilli())
	rows := []tasks.Row{{
		ID: "session", Title: "Frontend Bot", Status: tasks.Done, Local: true,
		StartedAt: &now, Outcome: "success", LastResult: &result,
	}}
	source := TaskSource{
		List:   func(context.Context) ([]tasks.Row, error) { return rows, nil },
		Result: func(_ context.Context, row tasks.Row) string { return *row.LastResult },
	}
	service := Open(ctx, path, transport, source, func() {})
	p := &profile{Account: Account{Token: "token", BotID: "bot", OwnerID: "owner"},
		BoundAt: int64(now) - 1, ContextToken: "context"}
	p.initMaps()
	service.update(ctx, p.status("reconnecting", ""))
	service.checkTasks(ctx, p)
	if len(p.Completions) != 1 || len(transport.sent) != 0 {
		t.Fatal("completion was not retained while WeChat was unreachable")
	}
	rows = nil
	service = Open(ctx, path, transport, source, func() {})
	if service.restored == nil {
		t.Fatal("completion queue could not be restored")
	}
	p = service.restored
	service.update(ctx, p.status("connected", ""))
	service.checkTasks(ctx, p)
	if reply := nextText(t, transport.sent); !strings.Contains(reply, result) {
		t.Fatalf("unexpected completion: %q", reply)
	}
	service.checkTasks(ctx, p)
	if len(p.Completions) != 0 || len(transport.sent) != 0 {
		t.Fatal("completion was retained or sent twice after acknowledgement")
	}
}

func nextText(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case value := <-messages:
		return value
	case <-time.After(4 * time.Second):
		t.Fatal("no outgoing message")
		return ""
	}
}

func waitPhase(t *testing.T, service *Service, expected string) Status {
	t.Helper()
	for deadline := time.Now().Add(4 * time.Second); time.Now().Before(deadline); {
		if status := service.Snapshot(); status.Phase == expected {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("wanted phase %s, got %#v", expected, service.Snapshot())
	return Status{}
}

type fixtureCore struct {
	mu       sync.Mutex
	row      tasks.Row
	question tasks.Question
}

func (f *fixtureCore) api() TaskSource {
	return TaskSource{
		List: func(context.Context) ([]tasks.Row, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return []tasks.Row{f.row}, nil
		},
		Prompt: func(_ context.Context, row tasks.Row) (tasks.Prompt, error) {
			if row.Status != tasks.Waiting {
				return tasks.Prompt{}, tasks.ErrNoQuickQuestion
			}
			return tasks.Prompt{Question: &f.question}, nil
		},
		Answer: func(_ context.Context, row tasks.Row, answer tasks.Answer) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.row.Status != tasks.Waiting || row.ID != f.row.ID || answer.QuestionID != "q" ||
				answer.Revision != 1 || len(answer.Selections) != 1 || answer.Selections[0].OptionIDs[0] != "mock" {
				return errors.New("incorrect or duplicate answer")
			}
			f.row.Status = tasks.Working
			return nil
		},
		FollowUp: func(context.Context, tasks.Row, string) error { return nil },
		Result: func(_ context.Context, row tasks.Row) string {
			if row.LastResult == nil {
				return ""
			}
			return *row.LastResult
		},
	}
}

type fixtureTransport struct {
	dns  chan Inbound
	sent chan string
}

func (f *fixtureTransport) StartQR(context.Context) (QR, error) {
	return QR{ID: "qr", Content: "https://weixin.qq.com/bind"}, nil
}
func (f *fixtureTransport) PollQR(context.Context, string, string, string) (Scan, error) {
	return Scan{Status: "confirmed", Account: Account{Token: "token", BotID: "bot", OwnerID: "owner"}}, nil
}
func (f *fixtureTransport) Updates(ctx context.Context, _ Account, cursor string) ([]Inbound, string, error) {
	select {
	case item := <-f.dns:
		return []Inbound{item}, item.ID, nil
	case <-ctx.Done():
		return nil, cursor, ctx.Err()
	}
}
func (f *fixtureTransport) Send(_ context.Context, _ Account, session, id, text string) error {
	if session != "context" || id == "" {
		return errors.New("wrong delivery scope")
	}
	f.sent <- text
	return nil
}

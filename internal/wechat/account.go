package wechat

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"orca-wails/internal/tasks"
)

type updateBatch struct {
	messages []Inbound
	cursor   string
	err      error
	accept   chan bool
}

func (s *Service) poll(ctx context.Context, p *profile, batches chan<- updateBatch) {
	account, cursor := p.Account, p.Cursor
	for ctx.Err() == nil {
		messages, next, err := s.transport.Updates(ctx, account, cursor)
		accept := make(chan bool, 1)
		select {
		case batches <- updateBatch{messages, next, err, accept}:
		case <-ctx.Done():
			return
		}
		select {
		case ok := <-accept:
			if ok && next != "" {
				cursor = next
			}
		case <-ctx.Done():
			return
		}
		if !pause(ctx, pollDelay(err)) {
			return
		}
	}
}

func pollDelay(err error) time.Duration {
	if err != nil {
		return 3 * time.Second
	}
	return 150 * time.Millisecond
}

func (s *Service) runAccount(session context.Context, p *profile, code string, test, refresh <-chan struct{}) {
	ctx, cancel := context.WithCancel(session)
	defer cancel()
	batches := make(chan updateBatch)
	// Copy the cursor before the account goroutine starts saving new batches.
	pollProfile := &profile{Account: p.Account, Cursor: p.Cursor}
	go s.poll(ctx, pollProfile, batches)
	timer := time.NewTimer(4 * time.Minute)
	defer timer.Stop()
	var expiry <-chan time.Time
	if code != "" {
		expiry = timer.C
	}
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry:
			s.pairFailed(ctx, "配对码已过期，请重新扫码。")
			return
		case batch := <-batches:
			if errors.Is(batch.err, ErrSessionExpired) {
				s.pairFailed(ctx, "微信连接已失效，请重新扫码。")
				return
			}
			if batch.err != nil {
				if p.BoundAt != 0 {
					s.update(ctx, p.status("reconnecting", "微信暂时无法连接，正在重试。"))
				}
				batch.accept <- false
				continue
			}
			saved := true
			for _, msg := range batch.messages {
				if err := s.receive(ctx, p, code, msg); err != nil {
					saved = false
					break
				}
				if p.BoundAt != 0 {
					expiry = nil
					code = ""
				}
			}
			if saved && batch.cursor != "" && p.Cursor != batch.cursor {
				last := p.Cursor
				p.Cursor = batch.cursor
				if err := s.save(ctx, p); err != nil {
					saved = false
					p.Cursor = last
				}
			}
			batch.accept <- saved
			if saved && p.BoundAt != 0 && s.Snapshot().Phase == "reconnecting" {
				s.update(ctx, p.status("connected", p.DeliveryIssue))
			}
		case <-ticker.C:
			s.checkTasks(ctx, p)
		case <-refresh:
			s.checkTasks(ctx, p)
		case <-test:
			if p.BoundAt != 0 {
				key := "test/" + strconv.FormatInt(time.Now().UnixNano(), 10)
				err := s.send(ctx, p, key, "Orca 测试通知\n等任务卡住或完成时，结果会发到这里。")
				if err == nil {
					s.update(ctx, p.status("connected", "测试通知已提交给微信。"))
				}
			}
		}
	}
}

func (p *profile) status(phase, message string) Status {
	return Status{Phase: phase, Message: message, LastReceived: p.LastReceived, LastSent: p.LastSent}
}

func (s *Service) receive(ctx context.Context, p *profile, code string, msg Inbound) error {
	if msg.From != p.Account.OwnerID || (msg.To != "" && msg.To != p.Account.BotID) || msg.ID == "" ||
		msg.Text == "" || len(msg.Text) > 4096 || slices.Contains(p.Received, msg.ID) {
		return nil
	}
	replyKey := "message/" + msg.ID
	if p.BoundAt == 0 {
		if msg.ContextToken == "" {
			return nil
		}
		value := strings.Join(strings.Fields(msg.Text), "")
		if value != code {
			s.update(ctx, Status{Phase: "verify", Code: code, Message: "配对码不匹配，请在微信中重试。"})
			return nil
		}
		p.BoundAt, p.ContextToken = time.Now().UnixMilli(), msg.ContextToken
		if err := s.recordInbound(ctx, p, msg); err != nil {
			p.BoundAt, p.ContextToken = 0, ""
			return err
		}
		s.update(ctx, p.status("connected", ""))
		_ = s.send(ctx, p, replyKey, "已绑定个人微信。发送「任务」查看进度。阻塞提醒和完成结果会发到这个私聊。")
		return nil
	}
	if err := s.recordInbound(ctx, p, msg); err != nil {
		s.update(ctx, p.status("connected", "无法保存最新消息，请检查 Orca 数据目录。"))
		return err
	}
	reply := s.respond(ctx, p, strings.TrimSpace(msg.Text))
	if err := s.save(ctx, p); err != nil {
		return err
	}
	_ = s.send(ctx, p, replyKey, reply)
	return nil
}

func (s *Service) recordInbound(ctx context.Context, p *profile, msg Inbound) error {
	previous, lastReceived, previousToken := p.Received, p.LastReceived, p.ContextToken
	if msg.ContextToken != "" {
		p.ContextToken = msg.ContextToken
	}
	p.LastReceived = time.Now().Unix()
	const maxRemembered = 512
	if len(p.Received) == maxRemembered {
		p.Received = p.Received[1:]
	}
	p.Received = append(p.Received, msg.ID)
	if err := s.save(ctx, p); err != nil {
		p.Received, p.LastReceived, p.ContextToken = previous, lastReceived, previousToken
		return err
	}
	return nil
}

func (s *Service) send(ctx context.Context, p *profile, key, message string) error {
	sum := sha256.Sum256([]byte(p.Account.BotID + "\x00" + key))
	err := s.transport.Send(ctx, p.Account, p.ContextToken, fmt.Sprintf("orca-%x", sum[:16]), message)
	switch {
	case err == nil:
		p.LastSent = time.Now().Unix()
		p.DeliveryIssue = ""
		_ = s.save(ctx, p)
		s.update(ctx, p.status("connected", ""))
	case errors.Is(err, ErrSessionExpired):
		s.pairFailed(ctx, "微信连接已失效，请重新扫码。")
	case errors.Is(err, ErrSendUncertain):
		p.DeliveryIssue = "微信未确认上一条消息，可在微信中查询任务结果。"
		_ = s.save(ctx, p)
		s.update(ctx, p.status("connected", p.DeliveryIssue))
	default:
		s.update(ctx, p.status("reconnecting", "微信未确认送达，可在任务列表查看最新状态。"))
	}
	return err
}

func (s *Service) checkTasks(ctx context.Context, p *profile) {
	if p.BoundAt == 0 {
		return
	}
	connected := s.Snapshot().Phase == "connected"
	rows, _ := s.tasks.List(ctx)
	type notice struct{ key, text string }
	var waiting []notice
	for _, task := range p.tasks(rows) {
		if ctx.Err() != nil {
			return
		}
		if !connected && task.Row.Status != tasks.Done {
			continue
		}
		key, text := s.notice(ctx, p, task)
		if key == "" || p.Notified[key] != 0 {
			continue
		}
		if task.Row.Status == tasks.Done {
			if _, exists := p.Completions[key]; !exists {
				p.Completions[key] = text
				if err := s.save(ctx, p); err != nil {
					delete(p.Completions, key)
					return
				}
			}
		} else {
			waiting = append(waiting, notice{key, text})
		}
	}
	if !connected {
		return
	}
	queued := make([]string, 0, len(p.Completions))
	for key := range p.Completions {
		queued = append(queued, key)
	}
	sort.Strings(queued)
	for _, key := range queued {
		if !s.sendNotice(ctx, p, key, p.Completions[key]) {
			return
		}
		delete(p.Completions, key)
		_ = s.save(ctx, p)
	}
	for _, item := range waiting {
		if !s.sendNotice(ctx, p, item.key, item.text) {
			return
		}
	}
}

func (s *Service) sendNotice(ctx context.Context, p *profile, key, text string) bool {
	if p.Notified[key] != 0 {
		return true
	}
	// Persist task/reply codes before they can leave the process.
	if err := s.save(ctx, p); err != nil {
		return false
	}
	err := s.send(ctx, p, key, text)
	if err == nil || errors.Is(err, ErrSendUncertain) {
		// A request may already have reached WeChat. Avoid replaying it without an acknowledgement API.
		p.Notified[key] = time.Now().Unix()
		_ = s.save(ctx, p)
		return true
	}
	return false
}

func (p *profile) tasks(rows []tasks.Row) []Task {
	result := make([]Task, 0, len(rows))
	for _, row := range rows {
		if !row.Local || row.ID == "" {
			continue
		}
		code := p.TaskCodes[row.ID]
		if code == "" {
			p.NextTask++
			code = fmt.Sprintf("A%d", p.NextTask)
			if p.Epoch > 1 {
				code = fmt.Sprintf("A%s.%d", strings.ToUpper(strconv.FormatUint(p.Epoch, 36)), p.NextTask)
			}
			p.TaskCodes[row.ID] = code
		}
		result = append(result, Task{Code: code, Row: row})
	}
	return result
}

func (p *profile) question(task Task, q tasks.Question) ReplyDraft {
	for _, entry := range p.Prompts {
		if entry.TaskID == task.Row.ID && entry.Draft.Question.ID == q.ID && entry.Draft.Question.Revision == q.Revision {
			return entry.Draft
		}
	}
	for code, entry := range p.Prompts {
		if entry.TaskID == task.Row.ID {
			delete(p.Prompts, code)
		}
	}
	p.NextPrompt++
	code := fmt.Sprintf("%s-%d", task.Code, p.NextPrompt)
	draft := ReplyDraft{Code: code, Question: q}
	p.Prompts[code] = savedPrompt{TaskID: task.Row.ID, Draft: draft}
	return draft
}

func (s *Service) notice(ctx context.Context, p *profile, task Task) (key, message string) {
	row := task.Row
	var stamp uint64
	if row.StartedAt != nil {
		stamp = *row.StartedAt
	} else if row.UpdatedAt != nil {
		stamp = *row.UpdatedAt
	}
	switch row.Status {
	case tasks.Done:
		if stamp < uint64(p.BoundAt) {
			return "", ""
		}
		key = fmt.Sprintf("%s/done/%d", row.ID, stamp)
		if _, queued := p.Completions[key]; p.Notified[key] == 0 && !queued {
			message = Completion(task, s.tasks.Result(ctx, row))
		}
	case tasks.Waiting, tasks.Blocked:
		if row.QuickReply {
			prompt, err := s.tasks.Prompt(ctx, row)
			if err != nil && !errors.Is(err, tasks.ErrNoQuickQuestion) {
				return "", ""
			}
			switch {
			case prompt.Question != nil:
				draft := p.question(task, *prompt.Question)
				return draft.Code + "/question", QuestionText(task, draft)
			case prompt.Approval != nil:
				return fmt.Sprintf("%s/%s/%d", row.ID, prompt.Approval.ID, prompt.Approval.Revision),
					TaskDetails(task, "需要工具审批，请在 Orca 原会话处理。")
			}
		}
		key = fmt.Sprintf("%s/%s/%d", row.ID, row.Status, stamp)
		message = TaskDetails(task, "请在 Orca 原会话查看并处理。")
	}
	return key, message
}

func (s *Service) respond(ctx context.Context, p *profile, text string) string {
	text = strings.ReplaceAll(text, "　", " ")
	if text == "帮助" {
		return "发送「任务」查看任务列表。发送「任务 A1」查看详情。按阻塞提醒里的回复码回答。"
	}
	rows, err := s.tasks.List(ctx)
	if err != nil {
		return "Orca Core 暂时无法连接，请稍后重试。"
	}
	list := p.tasks(rows)
	if text == "任务" || text == "进度" {
		return TaskList(list)
	}
	word, argument, _ := strings.Cut(text, " ")
	word, argument = strings.ToUpper(word), strings.TrimSpace(argument)
	if word == "任务" {
		for _, task := range list {
			if task.Code == strings.ToUpper(argument) {
				return s.taskDetails(ctx, p, task)
			}
		}
		return "找不到这项任务。发送「任务」刷新列表。"
	}
	if saved, ok := p.Prompts[word]; ok {
		for _, task := range list {
			if saved.TaskID == task.Row.ID {
				return s.reply(ctx, p, task, saved.Draft, argument)
			}
		}
		return "这条回复已过期。发送「任务」查看当前状态。"
	}
	if strings.Contains(word, "-") && strings.HasPrefix(word, "A") {
		return "这条回复已过期。发送「任务」查看当前状态。"
	}
	if addition, ok := strings.CutPrefix(argument, "补充 "); ok && strings.TrimSpace(addition) != "" {
		for _, task := range list {
			if task.Code == word {
				if err := s.tasks.FollowUp(ctx, task.Row, strings.TrimSpace(addition)); err != nil {
					return userError(err)
				}
				return "补充说明已提交到 " + word + " 的原会话。"
			}
		}
	}
	return "发送「任务」查看进度，或按照阻塞通知里的回复格式发送。"
}

func (s *Service) taskDetails(ctx context.Context, p *profile, task Task) string {
	if task.Row.QuickReply {
		prompt, _ := s.tasks.Prompt(ctx, task.Row)
		if prompt.Question != nil {
			draft := p.question(task, *prompt.Question)
			if s.save(ctx, p) == nil {
				return QuestionText(task, draft)
			}
			return "无法保存回复码，请检查 Orca 数据目录。"
		}
		if prompt.Approval != nil {
			return TaskDetails(task, "需要工具审批，请在 Orca 原会话处理。")
		}
	}
	result := s.tasks.Result(ctx, task.Row)
	if task.Row.Status == tasks.Done {
		return Completion(task, result)
	}
	if task.Row.CanFollowUp() && !task.Row.QuickReply {
		return TaskDetails(task, result) + "\n\n可发送「" + task.Code + " 补充 说明」追加指令。"
	}
	if task.Row.Status == tasks.Blocked || task.Row.Status == tasks.Waiting {
		return TaskDetails(task, result) + "\n\n请在 Orca 原会话查看并处理。"
	}
	return TaskDetails(task, result)
}

func (s *Service) reply(ctx context.Context, p *profile, task Task, draft ReplyDraft, text string) string {
	if !task.Row.QuickReply {
		return "这条回复已过期。发送「任务」查看当前状态。"
	}
	prompt, err := s.tasks.Prompt(ctx, task.Row)
	if err != nil && !errors.Is(err, tasks.ErrNoQuickQuestion) && !errors.Is(err, tasks.ErrQuestionChanged) {
		return userError(err)
	}
	if prompt.Question == nil || prompt.Question.ID != draft.Question.ID || prompt.Question.Revision != draft.Question.Revision {
		return "这条回复已过期。发送「任务 " + task.Code + "」查看当前问题。"
	}
	next, answer, err := ParseReply(draft, text)
	if err != nil {
		return "答案与当前问题不匹配，请按以下格式回复。\n\n" + QuestionText(task, draft)
	}
	if answer != nil {
		if err := s.tasks.Answer(ctx, task.Row, *answer); err != nil {
			return userError(err)
		}
		return "已提交 " + task.Code + " 的回复，等待 Bot 继续。发送「任务」查看进度。"
	}
	p.Prompts[draft.Code] = savedPrompt{TaskID: task.Row.ID, Draft: next}
	if err := s.save(ctx, p); err != nil {
		p.Prompts[draft.Code] = savedPrompt{TaskID: task.Row.ID, Draft: draft}
		return "无法保存分组答案，请检查 Orca 数据目录后重试。"
	}
	return QuestionText(task, next)
}

func userError(err error) string {
	switch {
	case errors.Is(err, tasks.ErrQuestionChanged):
		return "这条回复已过期，请发送「任务」查看当前问题。"
	case errors.Is(err, ErrPromptSettled):
		return "Orca 显示该问题已处理。发送「任务」查看进度。"
	case errors.Is(err, tasks.ErrAnswerUncertain):
		return "提交结果待确认。请先在 Orca 原会话核对。"
	case errors.Is(err, tasks.ErrFollowUpBlocked):
		return "会话已有待处理事项。请先处理原问题。"
	case errors.Is(err, tasks.ErrNoQuickQuestion):
		return "请在 Orca 原会话查看并处理。"
	default:
		return "Orca 未能处理这条请求。请刷新任务状态后重试。"
	}
}

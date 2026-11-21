package main

import (
	"encoding/json"
	"errors"

	"orca-wails/internal/tasks"
)

type trayInteractionRequest struct {
	RequestID string        `json:"requestID"`
	TaskID    string        `json:"taskID"`
	BotID     string        `json:"botID"`
	Answer    *tasks.Answer `json:"answer"`
}

type trayInteractionResult struct {
	RequestID         string          `json:"requestID"`
	Bots              *[]tasks.Bot    `json:"bots,omitempty"`
	Question          *tasks.Question `json:"question,omitempty"`
	Message           string          `json:"message,omitempty"`
	ReopenRequired    bool            `json:"reopenRequired,omitempty"`
	RepliedTaskID     string          `json:"repliedTaskID,omitempty"`
	RepliedQuestionID string          `json:"repliedQuestionID,omitempty"`
}

func (d *desktop) nativeTrayInteraction(action, arg string) {
	var request trayInteractionRequest
	if json.Unmarshal([]byte(arg), &request) != nil || len(request.RequestID) != 36 {
		return
	}

	result := trayInteractionResult{RequestID: request.RequestID}
	if len(arg) > 400*1024 {
		result.Message = "回复内容超出长度限制"
		d.sendNativeInteraction(result)
		return
	}
	// Restart and stop use the same lock. Resolve each request against the active Core.
	if action == "answer_question" || action == "open_bot_chat" {
		d.operation.Lock()
		defer d.operation.Unlock()
	}
	status := d.core.Status()
	if d.quitting.Load() || status.Phase != "running" || status.PID <= 0 {
		result.Message = "内核未运行，请启动后重试"
		d.sendNativeInteraction(result)
		return
	}

	data, pid := d.paths.data, uint32(status.PID)
	switch action {
	case "list_bots":
		bots, err := tasks.ListBots(d.ctx, data, pid)
		if err != nil {
			result.Message = "暂时无法读取 Bot 列表"
		} else {
			result.Bots = &bots
		}
	case "open_bot_chat":
		target, err := tasks.StartBotChat(d.ctx, data, pid, request.BotID)
		switch {
		case err != nil:
			result.Message = err.Error()
		case d.showOrca(false, &target, "open") != nil:
			result.Message = tasks.ErrBotOpen.Error()
		}
	case "read_question", "answer_question":
		sessionID := d.trayQuestionSession(request.TaskID, status.PID)
		if sessionID == "" {
			result.Message = tasks.ErrNoQuickQuestion.Error()
			break
		}
		if action == "read_question" {
			question, err := tasks.ReadQuestion(d.ctx, data, pid, sessionID)
			if err != nil {
				result.Message = "暂时无法读取这个问题，请在原会话中查看"
				if errors.Is(err, tasks.ErrNoQuickQuestion) {
					result.Message = tasks.ErrNoQuickQuestion.Error()
				}
			} else {
				result.Question = question
			}
		} else if request.Answer != nil {
			err := tasks.AnswerQuestion(d.ctx, data, pid, sessionID, *request.Answer)
			if err == nil {
				result.RepliedTaskID, result.RepliedQuestionID = request.TaskID, request.Answer.QuestionID
				select {
				case d.retryTasks <- struct{}{}:
				default:
				}
			} else {
				result.Message = "暂时无法发送，请打开原会话查看"
				switch {
				case errors.Is(err, tasks.ErrAnswerUncertain),
					errors.Is(err, tasks.ErrQuestionChanged),
					errors.Is(err, tasks.ErrAnswerRefused),
					errors.Is(err, tasks.ErrNoQuickQuestion):
					result.Message = err.Error()
				}
				result.ReopenRequired = true
			}
		} else {
			result.Message = "回复格式无效"
		}
	}
	d.sendNativeInteraction(result)
}

func (d *desktop) trayQuestionSession(id string, pid int) string {
	d.statusMu.RLock()
	defer d.statusMu.RUnlock()
	if id == "" || pid != d.taskPID || d.taskSummary == nil {
		return ""
	}

	for _, row := range d.taskSummary.Rows {
		if row.ID == id && row.QuickReply {
			return row.Target.StructuredSessionID()
		}
	}
	return ""
}

package main

import (
	"encoding/json"
	"errors"
	"slices"

	"orca-wails/internal/tasks"
)

type trayInteractionRequest struct {
	RequestID   string             `json:"requestID"`
	TaskID      string             `json:"taskID"`
	BotID       string             `json:"botID"`
	Answer      *tasks.Answer      `json:"answer"`
	Since       int64              `json:"since"`
	Text        string             `json:"text"`
	Launch      tasks.LaunchInput  `json:"launch"`
	LaunchCheck tasks.LaunchLookup `json:"launchCheck"`
	OperationID string             `json:"operationID"`
	File        string             `json:"file"`
	Area        string             `json:"area"`
}

type trayInteractionResult struct {
	RequestID         string               `json:"requestID"`
	Bots              *[]tasks.Bot         `json:"bots,omitempty"`
	Prompt            *tasks.Prompt        `json:"prompt,omitempty"`
	Detail            *tasks.TaskDetail    `json:"detail,omitempty"`
	Catalog           *tasks.LaunchCatalog `json:"catalog,omitempty"`
	Launch            *tasks.LaunchReceipt `json:"launch,omitempty"`
	LaunchCheck       *tasks.LaunchLookup  `json:"launchCheck,omitempty"`
	OperationID       string               `json:"operationID,omitempty"`
	Changes           *tasks.TrayChanges   `json:"changes,omitempty"`
	Diff              *tasks.TrayDiff      `json:"diff,omitempty"`
	Message           string               `json:"message,omitempty"`
	Receipt           string               `json:"receipt,omitempty"`
	Uncertain         bool                 `json:"uncertain,omitempty"`
	ReopenRequired    bool                 `json:"reopenRequired,omitempty"`
	RepliedTaskID     string               `json:"repliedTaskID,omitempty"`
	RepliedQuestionID string               `json:"repliedQuestionID,omitempty"`
	SettledTaskID     string               `json:"settledTaskID,omitempty"`
	SettledQuestionID string               `json:"settledQuestionID,omitempty"`
}

func (d *desktop) nativeTrayInteraction(action, arg string) {
	var request trayInteractionRequest
	if json.Unmarshal([]byte(arg), &request) != nil || len(request.RequestID) != 36 {
		return
	}

	result := trayInteractionResult{RequestID: request.RequestID}
	if len(arg) > 400*1024 {
		result.Message = tasks.ErrAnswerTooLarge.Error()
		d.sendNativeInteraction(result)
		return
	}
	// Restart and stop use the same lock. Resolve each request against the active Core.
	switch action {
	case "answer_question", "open_bot_chat", "send_follow_up", "launch_task", "open_launched_surface":
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
	refreshTasks := false
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
	case "read_task":
		row, err := d.trayInteractionRow(request.TaskID, status.PID)
		if err != nil {
			result.Message = "暂时无法确认任务状态，请重新读取"
		} else if row == nil {
			result.Message = "任务已不在列表中，可能已关闭或归档"
		} else {
			detail := tasks.ReadTaskDetail(d.ctx, data, pid, *row)
			result.Detail = &detail
		}
	case "read_changes", "read_diff":
		row, err := d.trayInteractionRow(request.TaskID, status.PID)
		if err != nil || row == nil {
			result.Message = tasks.ErrTrayChanges.Error()
			break
		}
		if action == "read_changes" {
			changes, err := tasks.ReadTrayChanges(d.ctx, data, pid, *row)
			if err != nil {
				result.Message = err.Error()
			} else {
				result.Changes = &changes
			}
		} else {
			diff, err := tasks.ReadTrayDiff(d.ctx, data, pid, *row, request.File, request.Area)
			if err != nil {
				result.Message = err.Error()
			} else {
				result.Diff = &diff
			}
		}
	case "launch_catalog":
		catalog, err := tasks.ReadLaunchCatalog(d.ctx, data, pid)
		if err != nil {
			result.Message = "暂时无法读取工作区和 Agent，请重试"
		} else {
			result.Catalog = &catalog
		}
	case "launch_task":
		receipt, err := tasks.LaunchTask(d.ctx, data, pid, request.Launch)
		if err != nil {
			result.Message = "暂时无法启动任务"
			if errors.Is(err, tasks.ErrLaunchUncertain) || errors.Is(err, tasks.ErrLaunchSelection) ||
				errors.Is(err, tasks.ErrInvalidAnswer) || errors.Is(err, tasks.ErrAnswerTooLarge) {
				result.Message = err.Error()
			}
			result.Uncertain = errors.Is(err, tasks.ErrLaunchUncertain)
			result.LaunchCheck = receipt.Lookup
		} else {
			result.Launch = &receipt
		}
		refreshTasks = err == nil || result.Uncertain
	case "check_launch", "open_launched_surface":
		receipt, err := tasks.CheckLaunch(d.ctx, data, pid, request.LaunchCheck)
		switch {
		case err != nil:
			result.Message = "暂时无法核对启动结果，请重试"
		case receipt == nil:
			result.Message = "尚未在原工作区找到这项任务，启动结果待确认"
		case action == "open_launched_surface":
			if receipt.OpenTarget == nil || d.showOrca(false, receipt.OpenTarget, "open") != nil {
				result.Message = "暂时无法打开新会话，请重试"
			}
		default:
			result.Launch = receipt
			refreshTasks = true
		}
	case "read_prompt", "answer_question", "check_answer", "send_follow_up", "check_follow_up":
		row, _ := d.trayInteractionRow(request.TaskID, status.PID)
		sessionID := ""
		if row != nil && row.Local {
			sessionID = row.Target.StructuredSessionID()
		}
		if sessionID == "" {
			result.Message = tasks.ErrNoQuickQuestion.Error()
			result.ReopenRequired = action == "answer_question"
			result.Uncertain = action == "check_answer"
			break
		}
		switch action {
		case "read_prompt":
			prompt, err := tasks.ReadPrompt(d.ctx, data, pid, sessionID)
			if err != nil && !errors.Is(err, tasks.ErrNoQuickQuestion) {
				result.Message = "暂时无法读取待办，请重试或打开原会话"
			} else {
				result.Prompt = &prompt
			}
		case "send_follow_up":
			receipt, err := tasks.SubmitFollowUp(d.ctx, data, pid, sessionID, request.Text)
			if err != nil {
				result.Message = "暂时无法发送后续消息，请打开原会话查看"
				if errors.Is(err, tasks.ErrFollowUpBlocked) || errors.Is(err, tasks.ErrInvalidAnswer) ||
					errors.Is(err, tasks.ErrAnswerTooLarge) || errors.Is(err, tasks.ErrAnswerRefused) {
					result.Message = err.Error()
				}
				if errors.Is(err, tasks.ErrAnswerUncertain) {
					result.Uncertain = true
					result.OperationID = receipt.OperationID
					result.Message = "后续消息结果待确认"
				}
			} else {
				result.Receipt = receipt.Message
				refreshTasks = true
			}
		case "check_follow_up":
			recorded, err := tasks.CheckFollowUp(d.ctx, data, pid, sessionID, request.OperationID)
			switch {
			case err != nil:
				result.Message = "暂时无法核对后续消息，请重试"
			case !recorded:
				result.Message = "尚未在原会话找到这条消息的记录"
			default:
				result.Receipt = "已找到后续消息记录，可在会话记录中查看处理结果"
				refreshTasks = true
			}
		case "check_answer", "answer_question":
			if request.Answer == nil {
				result.Message = tasks.ErrInvalidAnswer.Error()
				break
			}
			var err error
			if action == "answer_question" {
				err = tasks.AnswerQuestion(d.ctx, data, pid, sessionID, *request.Answer)
			} else {
				record, _ := tasks.CheckAnswer(d.ctx, data, pid, sessionID, *request.Answer, request.Since)
				switch record {
				case tasks.PromptAlreadySettled:
					result.SettledTaskID, result.SettledQuestionID = request.TaskID, request.Answer.QuestionID
					result.Receipt = "原待办已结束，无法核对这次提交"
					refreshTasks = true
				case tasks.AnswerUnconfirmed:
					err = tasks.ErrAnswerUncertain
				}
			}
			if err == nil && result.SettledTaskID == "" {
				result.RepliedTaskID, result.RepliedQuestionID = request.TaskID, request.Answer.QuestionID
				refreshTasks = true
			} else if err != nil {
				result.Message, result.Uncertain, result.ReopenRequired = trayPromptError(err)
			}
		}
	}
	if refreshTasks {
		select {
		case d.retryTasks <- struct{}{}:
		default:
		}
	}
	d.sendNativeInteraction(result)
}

func trayPromptError(err error) (message string, uncertain, reopen bool) {
	switch {
	case errors.Is(err, tasks.ErrAnswerUncertain):
		return err.Error(), true, false
	case errors.Is(err, tasks.ErrInvalidAnswer), errors.Is(err, tasks.ErrAnswerTooLarge):
		return err.Error(), false, false
	case errors.Is(err, tasks.ErrAnswerRefused), errors.Is(err, tasks.ErrQuestionChanged), errors.Is(err, tasks.ErrNoQuickQuestion):
		return err.Error(), false, true
	default:
		return "暂时无法处理，请打开原会话查看", false, true
	}
}

func (d *desktop) trayInteractionRow(id string, pid int) (*tasks.Row, error) {
	if id == "" {
		return nil, nil
	}
	find := func(summary *tasks.Summary) *tasks.Row {
		if summary != nil {
			i := slices.IndexFunc(summary.Rows, func(row tasks.Row) bool { return row.ID == id })
			if i >= 0 {
				row := summary.Rows[i]
				return &row
			}
		}
		return nil
	}

	d.statusMu.RLock()
	var row *tasks.Row
	if pid == d.taskPID {
		row = find(d.taskSummary)
	}
	d.statusMu.RUnlock()
	if row != nil {
		return row, nil
	}
	// An alert can be selected before the first successful poll.
	summary, err := tasks.Read(d.ctx, d.paths.data, uint32(pid))
	if err != nil {
		return nil, err
	}
	row = find(&summary)
	if row == nil && summary.Partial {
		return nil, errors.New("task listing is incomplete")
	}
	return row, nil
}

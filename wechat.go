package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"orca-wails/internal/tasks"
	"orca-wails/internal/wechat"
)

func (d *desktop) openWeChat() {
	source := wechat.TaskSource{
		List: func(ctx context.Context) ([]tasks.Row, error) {
			status, err := d.wechatCore()
			if err != nil {
				return nil, err
			}
			d.statusMu.RLock()
			summary := d.taskSummary
			pid := d.taskPID
			d.statusMu.RUnlock()
			if summary != nil && uint32(pid) == status {
				return summary.Rows, nil
			}
			live, err := tasks.Read(ctx, d.paths.data, status)
			return live.Rows, err
		},
		Prompt: func(ctx context.Context, row tasks.Row) (tasks.Prompt, error) {
			pid, err := d.wechatCore()
			if err != nil {
				return tasks.Prompt{}, err
			}
			return tasks.ReadPrompt(tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey), d.paths.data, pid, row.Target.StructuredSessionID())
		},
		Answer: func(ctx context.Context, row tasks.Row, answer tasks.Answer) error {
			pid, err := d.wechatCore()
			if err != nil {
				return err
			}
			session := row.Target.StructuredSessionID()
			sentAt := time.Now().UnixMilli()
			ctx = tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey)
			err = tasks.AnswerQuestion(ctx, d.paths.data, pid, session, answer)
			if errors.Is(err, tasks.ErrAnswerUncertain) {
				if state, _ := tasks.CheckAnswer(ctx, d.paths.data, pid, session, answer, sentAt); state != tasks.AnswerUnconfirmed {
					d.retryTaskPoll()
					return wechat.ErrPromptSettled
				}
			}
			if err == nil {
				d.retryTaskPoll()
			}
			return err
		},
		FollowUp: func(ctx context.Context, row tasks.Row, text string) error {
			pid, err := d.wechatCore()
			if err != nil {
				return err
			}
			_, err = tasks.SendFollowUp(ctx, d.paths.data, pid, row.Target.StructuredSessionID(), text)
			if err == nil {
				d.retryTaskPoll()
			}
			return err
		},
		Result: func(ctx context.Context, row tasks.Row) string {
			pid, err := d.wechatCore()
			if err != nil || row.Status == tasks.Done {
				if row.LastResult != nil {
					return *row.LastResult
				}
				return ""
			}
			return tasks.ReadTaskDetail(tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey), d.paths.data, pid, row).Result
		},
	}
	d.wechat = wechat.Open(d.ctx, filepath.Join(d.paths.root, "wechat.json"), wechat.NewClient(), source, func() {
		d.refreshNativeTray(false)
	})
}

func (d *desktop) retryTaskPoll() {
	select {
	case d.retryTasks <- struct{}{}:
	default:
	}
}

func (d *desktop) wechatCore() (uint32, error) {
	status := d.core.Status()
	if status.Phase != "running" {
		return 0, errors.New("Orca Core 暂不可用")
	}
	return uint32(status.PID), nil
}

func (d *desktop) wechatTrayAction(action, arg string) {
	if d.wechat == nil {
		return
	}
	switch action {
	case "wechat_start":
		d.wechat.StartPair()
	case "wechat_pin":
		d.wechat.SubmitPIN(arg)
	case "wechat_test":
		d.wechat.Test()
	case "wechat_cancel", "wechat_disconnect":
		if err := d.wechat.Disconnect(); err != nil {
			d.setMessage("无法清除微信连接，请检查 Orca 数据目录。")
			d.refreshNativeTray(false)
		}
	}
}

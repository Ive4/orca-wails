package main

import (
	"encoding/json"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"

	"orca-wails/internal/tasks"
)

type trayNotice struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	TaskID string `json:"taskID,omitempty"`
	Group  string `json:"group"`
}

type trayVersions struct {
	core, upstream               string
	installed, rollbackAvailable bool
}

func (d *desktop) refreshNativeTray(readVersions bool) {
	if !d.nativeTray || d.quitting.Load() {
		return
	}
	d.nativeTrayMu.Lock()
	defer d.nativeTrayMu.Unlock()
	if readVersions || d.nativeTrayDetails == nil {
		current, _ := d.store.Current()
		previous, _ := d.store.Previous()
		versions := &trayVersions{installed: current != nil || d.seed != "", rollbackAvailable: previous != nil}
		if current != nil {
			versions.core = current.Manifest.DisplayVersion()
			versions.upstream = current.Manifest.Version
		}
		d.nativeTrayDetails = versions
	}
	versions := d.nativeTrayDetails
	snapshot := d.panelSnapshot("native")
	snapshot["appVersion"] = version
	snapshot["coreVersion"] = versions.core
	snapshot["checkApp"] = d.appUpdateEnabled
	snapshot["checkCore"] = d.cfg.UpdateRepository != ""
	snapshot["bundledCore"] = d.seed != ""
	snapshot["coreInstalled"] = versions.installed
	snapshot["rollbackAvailable"] = versions.rollbackAvailable
	if d.wechat != nil {
		snapshot["wechat"] = d.wechat.Snapshot()
	}
	official, _ := snapshot["officialVersion"].(string)
	snapshot["officialVersion"] = upstreamLabel(versions.upstream, official)
	d.sendNativeSnapshot(snapshot)
}

func (d *desktop) publishTaskChanges(change trayTaskChanges) {
	d.clearNativeNotices(change.cleared)
	if !d.notificationsEnabled() || d.quitting.Load() {
		return
	}
	if cues := change.sounds(); len(cues) > 0 {
		d.sendNativeTaskSounds(cues)
	}
	for _, row := range change.attention {
		state := "任务等待输入"
		if row.Status == tasks.Blocked {
			state = "任务受阻"
		}
		parts := []string{row.Title}
		if row.Workspace != "" {
			parts = append(parts, row.Workspace)
		}
		d.sendNativeNotice(trayNotice{Title: state, Body: strings.Join(parts, " · "), TaskID: row.ID, Group: "orca.tasks"})
	}
}

func (d *desktop) nativeTrayAction(action, arg string) {
	if d.quitting.Load() {
		return
	}
	switch action {
	case "wechat_start", "wechat_pin", "wechat_test", "wechat_cancel", "wechat_disconnect":
		d.wechatTrayAction(action, arg)
		return
	case "list_bots", "open_bot_chat", "read_prompt", "answer_question", "check_answer",
		"read_task", "read_changes", "read_diff", "send_follow_up", "check_follow_up", "launch_catalog",
		"launch_task", "check_launch", "open_launched_surface":
		d.nativeTrayInteraction(action, arg)
		return
	case "refresh":
		d.refreshNativeTray(true)
		return
	case "open_shell_settings":
		d.request(action, nil)
		return
	case "notification_open":
		d.dispatchRequest(desktopRequest{action: "open_notification", notificationID: arg})
		return
	case "management_closed":
		application.InvokeAsync(func() {
			w := d.window.Load()
			if w == nil || !w.ready.Load() {
				setDockVisible(false)
			}
		})
		return
	case "open_new_workspace", "open_workspace_search", "minimise_orca", "fullscreen_orca":
		application.InvokeAsync(func() { d.applyWindowMenuAction(action) })
		return
	case "open_task":
		if target := d.notificationTaskTarget(arg); target != nil {
			d.request("open", target)
		} else {
			d.setMessage("任务状态已变化，请刷新后重试")
			d.refreshNativeTray(false)
		}
		return
	case "notifications":
		if arg == "on" || arg == "off" {
			d.request("notifications_"+arg, nil)
		}
		return
	case "menu_bar_on", "menu_bar_off", "notch_on", "notch_off":
		if err := d.setMacOSEntry(action); err != nil {
			d.setMessage(err.Error())
		}
		d.refreshNativeTray(false)
		return
	case "computer_use":
		var settings computerUseSettings
		if err := json.Unmarshal([]byte(arg), &settings); err != nil {
			d.setMessage("无效的电脑操控设置")
		} else if err := d.setComputerUse(settings); err != nil {
			d.setMessage(err.Error())
		}
		d.refreshNativeTray(false)
		return
	case "global_shortcut":
		if err := d.setTrayShortcut(arg); err != nil {
			d.setMessage("快捷键设置：" + err.Error())
		} else {
			d.setMessage("快捷键已保存")
		}
		d.refreshNativeTray(false)
		return
	default:
		arg = ""
	}
	if err := d.runTrayAction(panelAction{Kind: action, ID: arg}, func() {}); err != nil {
		d.setMessage(err.Error())
		d.refreshNativeTray(false)
	}
}

func (d *desktop) applyWindowMenuAction(action string) {
	w := d.window.Load()
	if w == nil || !w.ready.Load() || d.quitting.Load() {
		return
	}
	switch action {
	case "open_new_workspace", "open_workspace_search":
		if !w.paired {
			w.win.UnMinimise()
			w.win.Show().Focus()
			if action == "open_new_workspace" {
				evalOrca(w.win, "window.__orcaTrayNewWorkspace?.();")
			} else {
				evalOrca(w.win, "window.__orcaTrayWorkspaceSearch?.();")
			}
		}
	case "minimise_orca":
		w.win.Minimise()
	case "fullscreen_orca":
		if w.win.IsFullscreen() {
			w.win.UnFullscreen()
		} else {
			w.win.Fullscreen()
		}
	}
}

func (d *desktop) notificationTaskTarget(id string) *tasks.Target {
	if target := d.trayTaskTarget(id); target != nil {
		return target
	}
	status := d.core.Status()
	if status.Phase != "running" {
		return nil
	}
	// A notification may launch the app before the first task poll.
	summary, err := tasks.Read(d.ctx, d.paths.data, uint32(status.PID))
	if err != nil {
		return nil
	}
	return taskTargetInSummary(&summary, id)
}

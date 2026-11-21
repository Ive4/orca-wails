package main

import (
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
	official, _ := snapshot["officialVersion"].(string)
	snapshot["officialVersion"] = upstreamLabel(versions.upstream, official)
	d.sendNativeSnapshot(snapshot)
}

func (d *desktop) publishTaskNotices(change trayTaskChanges) {
	d.clearNativeNotices(change.cleared)
	if !d.notificationsEnabled() {
		return
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
	case "list_bots", "open_bot_chat", "read_question", "answer_question":
		d.nativeTrayInteraction(action, arg)
		return
	case "refresh":
		d.refreshNativeTray(true)
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
		action = "open"
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

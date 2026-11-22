package main

import (
	"encoding/json"
	"errors"
)

func (d *desktop) shellSettingsSnapshot() map[string]any {
	state := d.panelSnapshot("native")
	delete(state, "summary")
	current, _ := d.store.Current()
	previous, _ := d.store.Previous()
	state["appVersion"] = version
	state["coreVersion"] = ""
	if current != nil {
		state["coreVersion"] = current.Manifest.DisplayVersion()
		official, _ := state["officialVersion"].(string)
		state["officialVersion"] = upstreamLabel(current.Manifest.Version, official)
	}
	state["checkApp"] = d.appUpdateEnabled
	state["checkCore"] = d.cfg.UpdateRepository != ""
	state["bundledCore"] = d.seed != ""
	state["coreInstalled"] = current != nil || d.seed != ""
	state["rollbackAvailable"] = previous != nil
	qr := ""
	if d.wechat != nil {
		status := d.wechat.Snapshot()
		qr = status.QR
		status.QR = ""
		state["wechat"] = status
	}
	state["native"] = nativeShellSettings("state", qr)
	return state
}

func (d *desktop) applyShellSettings(params json.RawMessage) error {
	var input struct {
		Kind  string              `json:"kind"`
		Value string              `json:"value"`
		CUA   computerUseSettings `json:"cua"`
	}
	if json.Unmarshal(params, &input) != nil || d.quitting.Load() {
		return errors.New("无效的桌面设置操作")
	}
	defer d.refreshNativeTray(false)
	switch input.Kind {
	case "computer_use":
		return d.setComputerUse(input.CUA)
	case "global_shortcut":
		return d.setTrayShortcut(input.Value)
	case "menu_bar_on", "menu_bar_off", "notch_on", "notch_off":
		return d.setMacOSEntry(input.Kind)
	case "notifications_on", "notifications_off":
		return d.execute(desktopRequest{action: input.Kind})
	case "notification_permission", "notification_system", "notification_test", "notification_sound", "notification_preview":
		nativeShellSettings(input.Kind, input.Value)
		return nil
	case "wechat_start", "wechat_pin", "wechat_test", "wechat_cancel", "wechat_disconnect":
		d.wechatTrayAction(input.Kind, input.Value)
		return nil
	case "autostart", "open_paired", "close_orca", "import_official", "check_app", "install_app",
		"check_core", "install_core", "check_official", "start", "stop", "restart", "rollback",
		"config", "logs", "data", "quit":
		return d.runTrayAction(panelAction{Kind: input.Kind}, func() {})
	default:
		return errors.New("未知的桌面设置操作")
	}
}

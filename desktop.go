package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"orca-wails/internal/core"
	"orca-wails/internal/official"
	"orca-wails/internal/tasks"
	"orca-wails/internal/update"
)

func (d *desktop) setMessage(message string) {
	d.statusMu.Lock()
	d.message = message
	d.statusMu.Unlock()
}

func (d *desktop) notificationsEnabled() bool {
	d.statusMu.RLock()
	defer d.statusMu.RUnlock()
	return d.notifications
}

func (d *desktop) setMacOSEntry(action string) error {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	cfg, _, err := loadConfig(d.paths.root)
	if err != nil {
		return err
	}
	if action == "menu_bar_on" || action == "menu_bar_off" {
		cfg.MacOSEntries.MenuBar = action == "menu_bar_on"
	} else {
		cfg.MacOSEntries.Notch = action == "notch_on"
	}
	if err := saveConfig(cfg, d.paths.settings); err != nil {
		return err
	}
	d.statusMu.Lock()
	d.entries = cfg.MacOSEntries
	d.message = "入口设置已保存"
	d.statusMu.Unlock()
	return nil
}

func (d *desktop) setComputerUse(settings computerUseSettings) error {
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	cfg, _, err := loadConfig(d.paths.root)
	if err != nil {
		return err
	}
	cfg.computerUseSettings = settings
	if err := saveConfig(cfg, d.paths.settings); err != nil {
		return err
	}
	d.statusMu.Lock()
	d.computerUse = settings
	d.message = "电脑操控设置已保存"
	d.statusMu.Unlock()
	return nil
}

func (d *desktop) notify(message string) {
	if !d.notificationsEnabled() || d.quitting.Load() {
		return
	}
	if d.nativeTray {
		d.sendNativeNotice(trayNotice{Title: appName, Body: message, Group: "orca.general"})
		return
	}
	notifyDesktop(message)
}

type desktopRequest struct {
	action         string
	task           *tasks.Target
	notificationID string
}

func (d *desktop) request(action string, task *tasks.Target) {
	d.dispatchRequest(desktopRequest{action: action, task: task})
}

func (d *desktop) dispatchRequest(request desktopRequest) {
	action, task := request.action, request.task
	if d.quitting.Load() {
		return
	}
	if d.busy.Load() && d.window.Load() != nil {
		switch action {
		case "close_orca", "toggle":
			go d.closeOrca()
			return
		case "open", "open_bots", "open_shell_settings":
			if w := d.window.Load(); w != nil && !w.paired {
				go func() { _ = d.showOrca(false, task, action) }()
				return
			}
		}
	}
	if !d.busy.CompareAndSwap(false, true) {
		switch action {
		case "open", "toggle":
			open := &desktopRequest{action: "open", task: task}
			if task != nil {
				d.pending.Store(open)
			} else {
				d.pending.CompareAndSwap(nil, open)
			}
		case "open_bots", "open_notification", "open_shell_settings":
			d.pending.Store(&request)
		}
		d.openPending()
		return
	}
	go func() {
		d.refreshNativeTray(false)
		d.operation.Lock()
		if !d.quitting.Load() {
			if err := d.execute(request); err != nil && !d.quitting.Load() {
				d.setMessage(err.Error())
				d.notify(err.Error())
				switch action {
				case "open", "open_paired", "open_bots", "open_notification", "open_shell_settings", "toggle":
					if d.window.Load() == nil {
						d.showNativeManagement()
					}
				}
			}
		}
		d.operation.Unlock()
		d.busy.Store(false)
		d.refreshNativeTray(true)
		d.openPending()
	}()
}

func (d *desktop) openPending() {
	if d.busy.Load() {
		return
	}
	if action := d.pending.Swap(nil); action != nil {
		d.dispatchRequest(*action)
	}
}

func (d *desktop) execute(request desktopRequest) error {
	action, task := request.action, request.task
	switch action {
	case "start":
		return d.ensureCore()
	case "toggle":
		if d.window.Load() != nil {
			d.closeOrca()
			return nil
		}
		fallthrough
	case "open", "open_paired", "open_bots", "open_notification", "open_shell_settings":
		if err := d.ensureCore(); err != nil {
			return err
		}
		if request.notificationID != "" {
			task = d.notificationTaskTarget(request.notificationID)
		}
		if err := d.showOrca(action == "open_paired", task, action); err != nil {
			return err
		}
		d.setMessage("窗口已打开")
	case "close_orca":
		d.closeOrca()
		d.setMessage("窗口已关闭")
	case "restart":
		reopen := d.closeOrca()
		d.setMessage("正在重启内核…")
		if err := d.core.Stop(d.ctx); err != nil {
			return err
		}
		if err := d.ensureCore(); err != nil {
			return err
		}
		d.reopen(reopen)
	case "stop":
		d.closeOrca()
		d.setMessage("正在停止内核…")
		if err := d.core.Stop(d.ctx); err != nil {
			return err
		}
		d.setMessage("内核已停止")
		d.showNativeManagement()
	case "check_app":
		return d.checkAppUpdate()
	case "install_app":
		return d.installAppUpdate()
	case "check_core":
		return d.checkCore()
	case "install_core":
		return d.installCore()
	case "rollback":
		slot, err := d.store.Previous()
		if err != nil {
			return err
		}
		if slot == nil {
			return errors.New("没有可回退的内核")
		}
		if err := d.switchCore(slot); err != nil {
			return err
		}
		d.setMessage("已回退到内核 " + slot.Manifest.DisplayVersion())
		d.notify("已回退到内核 " + slot.Manifest.DisplayVersion())
	case "check_official":
		d.setMessage("正在查询 Orca 官方版本…")
		latest, err := d.store.LatestOfficial(d.ctx)
		if err != nil {
			return err
		}
		d.statusMu.Lock()
		d.latestOfficial = latest
		d.message = "官方版本查询完成"
		d.statusMu.Unlock()
	case "import_official":
		return d.importOfficial()
	case "notifications_on", "notifications_off":
		d.settingsMu.Lock()
		defer d.settingsMu.Unlock()
		enabled := action == "notifications_on"
		cfg, _, err := loadConfig(d.paths.root)
		if err != nil {
			return err
		}
		cfg.Notifications = enabled
		if err := saveConfig(cfg, d.paths.settings); err != nil {
			return err
		}
		d.statusMu.Lock()
		d.notifications = enabled
		d.message = "通知设置已保存"
		d.statusMu.Unlock()
	case "autostart":
		d.settingsMu.Lock()
		defer d.settingsMu.Unlock()
		d.statusMu.RLock()
		next := !d.autostart
		d.statusMu.RUnlock()
		if err := setAutostart(next); err != nil {
			return err
		}
		cfg, _, err := loadConfig(d.paths.root)
		if err == nil {
			cfg.Autostart = next
			err = saveConfig(cfg, d.paths.settings)
		}
		if err != nil {
			return errors.Join(err, setAutostart(!next))
		}
		d.statusMu.Lock()
		d.autostart = next
		d.message = "开机启动设置已保存"
		d.statusMu.Unlock()
	default:
		return fmt.Errorf("未知操作 %q", action)
	}
	return nil
}

type seedIdentity struct {
	Version  string `json:"version"`
	Revision string `json:"package_revision"`
	Target   string `json:"target"`
	SHA256   string `json:"sha256"`
}

func readSeed(path string) (seedIdentity, error) {
	var id seedIdentity
	data, err := os.ReadFile(strings.TrimSuffix(path, filepath.Ext(path)) + ".json")
	if err != nil {
		return id, err
	}
	if err := json.Unmarshal(data, &id); err != nil {
		return id, err
	}
	if id.Version == "" || len(id.SHA256) != 64 || id.Target != targetTriple() {
		return id, errors.New("随附内核校验文件缺失或与平台不匹配")
	}
	return id, nil
}

func (d *desktop) ensureCore() error {
	if d.core.Status().Phase == "running" {
		return nil
	}
	current, err := d.store.Current()
	if err != nil {
		return err
	}
	if d.seed != "" {
		if activated, seedErr := d.activateSeed(current); activated {
			d.setMessage("内核运行中")
			return nil
		} else if seedErr != nil {
			if cleanup := d.core.Stop(context.Background()); cleanup != nil {
				return errors.Join(seedErr, cleanup)
			}
			if current == nil {
				return seedErr
			}
			if recovery := d.startSlot(current); recovery != nil {
				return errors.Join(seedErr, recovery)
			}
			d.setMessage("随附内核启动失败，已恢复原内核：" + seedErr.Error())
			return nil
		}
	}
	if current == nil {
		return errors.New("尚未安装内核。请随应用打包内核或通过托盘的「关于与更新」安装")
	}
	if err := d.startSlot(current); err != nil {
		return err
	}
	d.setMessage("内核运行中")
	return nil
}

func (d *desktop) activateSeed(current *update.Installed) (bool, error) {
	seed, err := readSeed(d.seed)
	if err != nil {
		return false, err
	}
	if current != nil && !current.Manifest.OlderThan(update.Manifest{Version: seed.Version, PackageRevision: seed.Revision}) {
		return false, nil
	}
	d.setMessage("正在校验随附内核…")
	slot, err := d.store.PrepareArchiveContext(d.ctx, d.seed, seed.SHA256)
	if err != nil {
		return false, err
	}
	if slot.Manifest.Version != seed.Version || slot.Manifest.PackageRevision != seed.Revision {
		return false, errors.New("随附内核版本与校验文件不匹配")
	}
	if err := d.startAndCommit(slot); err != nil {
		return false, err
	}
	return true, nil
}

func (d *desktop) coreLaunchSpec(cfg config, slot *update.Installed) (core.Spec, error) {
	spec, err := cfg.launchSpec(d.paths, slot)
	if err == nil {
		spec.DesktopQuestionKey = d.desktopAccessKey
		if slot.Manifest.DesktopProtocol == 1 {
			spec.Args = append(spec.Args, "--no-pairing")
			spec.Env["ORCA_WAILS_DESKTOP"] = "1"
		}
	}
	return spec, err
}

func (d *desktop) startSlot(slot *update.Installed) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	cfg := d.cfg
	spec, err := d.coreLaunchSpec(cfg, slot)
	if err != nil {
		return err
	}
	d.setMessage("正在启动内核…")
	err = d.core.Start(d.ctx, spec)
	if runtime.GOOS == "darwin" && cfg.Port == 6768 && errors.Is(err, core.ErrPortOccupied) {
		cfg.Port = 16768
		spec, err = d.coreLaunchSpec(cfg, slot)
		if err == nil {
			err = d.core.Start(d.ctx, spec)
		}
	}
	return err
}

func (d *desktop) startAndCommit(slot *update.Installed) error {
	if err := d.startSlot(slot); err != nil {
		return err
	}
	if slot.Manifest.DesktopProtocol == 1 {
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
		defer cancel()
		status := d.core.Status()
		ctx = tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey)
		result, err := tasks.CallLocal(ctx, d.paths.data, uint32(status.PID), "status.get", nil)
		var response struct {
			OK bool `json:"ok"`
		}
		if err != nil || json.Unmarshal(result, &response) != nil || !response.OK {
			return errors.New("候选内核未通过本机桌面连接验证")
		}
	} else {
		offer, port, ok := d.core.Offer()
		if !ok {
			return errors.New("候选内核没有提供可用的 Web 配对信息")
		}
		if _, err := validatedOffer(offer, port); err != nil {
			return err
		}
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	return d.store.Commit(slot)
}

func (d *desktop) checkCore() error {
	d.statusMu.Lock()
	d.coreUpdate = nil
	d.statusMu.Unlock()
	d.setMessage("正在检查内核更新…")
	release, err := d.store.LatestCore(d.ctx)
	if err != nil {
		return err
	}
	if release == nil {
		return errors.New("更新仓库中没有适用于当前平台的稳定内核包")
	}
	current, err := d.store.Current()
	if err != nil {
		return err
	}
	if current != nil && !release.NewerThan(current.Manifest) {
		d.setMessage("当前内核已是最新版本")
		return nil
	}
	d.statusMu.Lock()
	d.coreUpdate = release
	d.message = "发现内核更新 · " + release.DisplayVersion()
	d.statusMu.Unlock()
	return nil
}

func (d *desktop) installCore() error {
	d.statusMu.RLock()
	release := d.coreUpdate
	d.statusMu.RUnlock()
	if release == nil {
		return errors.New("请先检查内核更新")
	}
	d.setMessage("正在下载和校验内核 " + release.DisplayVersion() + "…")
	slot, err := d.store.PrepareRelease(d.ctx, release)
	if err != nil {
		return err
	}
	if err := d.switchCore(slot); err != nil {
		return err
	}
	d.statusMu.Lock()
	d.coreUpdate = nil
	d.message = "内核更新完成 · " + slot.Manifest.DisplayVersion()
	d.statusMu.Unlock()
	d.notify("内核已更新至 " + slot.Manifest.DisplayVersion())
	return nil
}

func (d *desktop) switchCore(slot *update.Installed) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	previous, err := d.store.Current()
	if err != nil {
		return err
	}
	wasRunning := d.core.Status().Phase == "running"
	reopen := d.closeOrca()
	if err := d.core.Stop(d.ctx); err != nil {
		return err
	}
	if err := d.startAndCommit(slot); err != nil {
		if cleanup := d.core.Stop(context.Background()); cleanup != nil {
			return errors.Join(err, fmt.Errorf("候选内核尚未退出: %w", cleanup))
		}
		if wasRunning && previous != nil && !d.quitting.Load() {
			if recovery := d.startSlot(previous); recovery != nil {
				return errors.Join(err, fmt.Errorf("恢复原内核: %w", recovery))
			}
			d.reopen(reopen)
		}
		return fmt.Errorf("候选内核未激活: %w", err)
	}
	d.reopen(reopen)
	return nil
}

func (d *desktop) importOfficial() error {
	d.setMessage("正在备份并导入官方 Orca 数据…")
	current, err := d.store.Current()
	if err != nil {
		return err
	}
	ver := ""
	if current != nil {
		ver = current.Manifest.Version
	} else if d.seed != "" {
		id, err := readSeed(d.seed)
		if err != nil {
			return err
		}
		ver = id.Version
	}
	if ver != official.Version {
		return fmt.Errorf("导入需要 Core %s", official.Version)
	}
	coreDir, err := filepath.EvalSymlinks(d.paths.core)
	if err != nil {
		return err
	}
	dataDir, err := filepath.EvalSymlinks(d.paths.data)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(dataDir, coreDir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("请将内核安装在数据目录之外")
	}
	source, err := official.Discover()
	if err != nil {
		return err
	}
	staging, err := official.Prepare(source, d.paths.data)
	if err != nil {
		return err
	}
	if err := d.ctx.Err(); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	reopen := d.closeOrca()
	wasRunning := d.core.Status().Phase == "running"
	if err := d.core.Stop(d.ctx); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := d.ctx.Err(); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	if err := official.Swap(dataDir, staging); err != nil {
		// A failed rename rollback may leave the original data staged.
		return errors.Join(err, d.restoreImport(reopen, wasRunning))
	}
	if err := d.ensureCore(); err != nil {
		if cleanup := d.core.Stop(context.Background()); cleanup != nil {
			return errors.Join(err, fmt.Errorf("数据备份在 %s；进程回收失败: %w", staging, cleanup))
		}
		if rollback := official.Swap(dataDir, staging); rollback != nil {
			return errors.Join(err, fmt.Errorf("数据备份在 %s；恢复失败: %w", staging, rollback))
		}
		_ = os.RemoveAll(staging)
		return errors.Join(fmt.Errorf("导入后启动失败，已恢复原数据: %w", err), d.restoreImport(reopen, wasRunning))
	}
	d.reopen(reopen)
	d.setMessage("导入完成 · 原数据备份：" + filepath.Base(staging))
	d.notify("官方 Orca 数据已导入")
	return nil
}

func (d *desktop) restoreImport(reopen *orcaWindow, running bool) error {
	if !running || d.quitting.Load() {
		return nil
	}
	if err := d.ensureCore(); err != nil {
		return err
	}
	d.reopen(reopen)
	return nil
}

func (d *desktop) reopen(w *orcaWindow) {
	if w != nil && !d.quitting.Load() {
		if err := d.showOrca(w.paired, nil, "open"); err != nil {
			d.setMessage(err.Error())
		}
	}
}

func (d *desktop) quit() {
	if !d.quitting.CompareAndSwap(false, true) {
		return
	}
	d.setMessage("正在退出并回收进程…")
	d.cancel()
	go func() {
		d.operation.Lock()
		defer d.operation.Unlock()
		d.closePanel(nil)
		d.closeOrca()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if err := d.core.Stop(ctx); err != nil {
			d.core.EmergencyStop()
		}
		d.quitReady.Store(true)
		d.app.Quit()
	}()
}

func (d *desktop) pollTasks() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	var macLights chan trayTaskLights
	if runtime.GOOS == "darwin" && !d.nativeTray {
		macLights = make(chan trayTaskLights, 1)
		go d.animateMacTray(macLights)
	}
	var lastLights trayTaskLights
	var tracker trayTaskTracker
	observedPID := 0
	updateLights := func(lights trayTaskLights) {
		if d.nativeTray {
			d.refreshNativeTray(false)
			return
		}
		if lights == lastLights {
			return
		}
		lastLights = lights
		if macLights == nil {
			d.setTrayLights(lights)
			return
		}
		select {
		case macLights <- lights:
		case <-d.ctx.Done():
		}
	}
	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
		case <-d.retryTasks:
		}
		status := d.core.Status()
		if status.Phase != "running" {
			d.statusMu.Lock()
			d.taskPID, d.taskSummary, d.taskAttempted = 0, nil, false
			d.statusMu.Unlock()
			if d.nativeTray && observedPID != 0 {
				_, change := tracker.observe(tasks.Summary{})
				d.clearNativeNotices(change.cleared)
			}
			observedPID, tracker = 0, trayTaskTracker{}
			updateLights(trayTaskLights{})
			continue
		}
		pid := status.PID
		summary, err := tasks.Read(d.ctx, d.paths.data, uint32(pid))
		if d.ctx.Err() != nil {
			return
		}
		if current := d.core.Status(); current.PID != pid || current.Phase != "running" {
			continue
		}
		d.statusMu.Lock()
		d.taskPID = pid
		d.taskAttempted = true
		d.taskSummary = nil
		if err == nil {
			d.taskSummary = &summary
		}
		d.statusMu.Unlock()
		if observedPID != pid {
			if d.nativeTray {
				_, change := tracker.observe(tasks.Summary{})
				d.clearNativeNotices(change.cleared)
			}
			observedPID, tracker = pid, trayTaskTracker{}
		}
		if err != nil {
			updateLights(trayTaskLights{unavailable: true})
			continue
		}
		if d.wechat != nil {
			d.wechat.Refresh()
		}
		lights, change := tracker.observe(summary)
		updateLights(lights)
		if d.nativeTray {
			d.publishTaskChanges(change)
		} else if notice := change.notice(); notice != "" {
			d.notify(notice)
		}
	}
}

package main

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
	"golang.org/x/mod/semver"

	"orca-wails/internal/tasks"
)

const panelWidth, panelHeight = 320, 420

//go:embed assets/tray/*
var panelAssets embed.FS

type trayPanel struct {
	win   *application.WebviewWindow
	token string
	shown atomic.Bool
}

type panelAction struct {
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Height int    `json:"height"`
}

func (d *desktop) configureTray() {
	d.tray = d.app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		d.tray.SetTemplateIcon(trayIcon)
	} else {
		d.tray.SetIcon(trayColorIcon)
	}
	d.tray.SetTooltip(appName)
	d.tray.OnClick(func() { go d.togglePanel() })
	menu := application.NewMenu()
	menu.Add("打开 Orca").OnClick(func(*application.Context) { d.request("open", nil) })
	menu.Add("打开任务面板").OnClick(func(*application.Context) { go d.togglePanel() })
	menu.Add("关闭 Orca 窗口").OnClick(func(*application.Context) { d.request("close_orca", nil) })
	menu.AddSeparator()
	menu.Add("退出 " + appName).OnClick(func(*application.Context) { d.quit() })
	d.tray.SetMenu(menu)
}

func (d *desktop) configureMenu() {
	menu := application.NewMenu()
	app := menu.AddSubmenu(appName)
	app.Add("打开 Orca").OnClick(func(*application.Context) { d.request("open", nil) })
	app.Add("关闭 Orca 窗口").OnClick(func(*application.Context) { d.request("close_orca", nil) })
	app.AddSeparator()
	app.Add("退出 " + appName).SetAccelerator("CmdOrCtrl+Q").OnClick(func(*application.Context) { d.quit() })
	menu.AddRole(application.EditMenu)
	d.app.Menu.SetApplicationMenu(menu)
}

func (d *desktop) togglePanel() {
	application.InvokeSync(func() {
		if d.quitting.Load() {
			return
		}
		if panel := d.panel.Load(); panel != nil {
			d.closePanel(panel)
			return
		}
		if time.Since(time.Unix(0, d.panelDismissedAt.Load())) < 175*time.Millisecond {
			return
		}
		p := &trayPanel{token: hex.EncodeToString(randomToken())}
		d.panel.Store(p)
		options := application.WebviewWindowOptions{
			Name: "orca-tray", Title: "Orca 任务", URL: "/tray/index.html#" + p.token,
			Width: panelWidth, Height: panelHeight, Hidden: true,
			Frameless: true, AlwaysOnTop: true, DisableResize: true,
			BackgroundType:             application.BackgroundTypeTransparent,
			DefaultContextMenuDisabled: true, Permissions: untrustedPermissions(),
		}
		if runtime.GOOS == "darwin" {
			options.Mac = application.MacWindow{
				WindowClass:  application.MacWindowClassPanel,
				Backdrop:     application.MacBackdropLiquidGlass,
				CornerRadius: 24,
				LiquidGlass: application.MacLiquidGlass{
					Material:     application.NSVisualEffectMaterialPopover,
					CornerRadius: 24,
				},
			}
			options.JS = `document.body.setAttribute("data-native-material", "");`
		}
		p.win = d.app.Window.NewWithOptions(options)
		p.win.OnWindowEvent(events.Common.WindowLostFocus, func(*application.WindowEvent) {
			if p.shown.Load() && d.panel.Load() == p {
				d.panelDismissedAt.Store(time.Now().UnixNano())
				go d.closePanel(p)
			}
		})
		p.win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			d.panel.CompareAndSwap(p, nil)
		})
		time.AfterFunc(8*time.Second, func() {
			if !p.shown.Load() {
				d.closePanel(p)
			}
		})
	})
}

func randomToken() []byte {
	var token [32]byte
	rand.Read(token[:])
	return token[:]
}

func (d *desktop) closePanel(expected *trayPanel) {
	application.InvokeSync(func() {
		p := d.panel.Load()
		if p == nil || (expected != nil && p != expected) {
			return
		}
		if d.panel.CompareAndSwap(p, nil) {
			p.win.Close()
		}
	})
}

func (d *desktop) serveAssets(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	switch r.URL.Path {
	case "/tray/index.html", "/tray/panel.css", "/tray/panel.js":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := panelAssets.ReadFile("assets" + r.URL.Path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		switch filepath.Ext(r.URL.Path) {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		}
		_, _ = w.Write(data)
	case "/tray/api":
		d.servePanelAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (d *desktop) servePanelAPI(w http.ResponseWriter, r *http.Request) {
	p := d.panel.Load()
	token := r.Header.Get("X-Orca-Panel")
	if p == nil || subtle.ConstantTimeCompare([]byte(p.token), []byte(token)) != 1 {
		http.Error(w, "此窗口不能操作托盘", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	switch r.Method {
	case http.MethodGet:
		_ = json.NewEncoder(w).Encode(d.panelSnapshot(r.URL.Query().Get("page")))
	case http.MethodPost:
		body := http.MaxBytesReader(w, r.Body, 4096)
		decode := json.NewDecoder(body)
		decode.DisallowUnknownFields()
		var action panelAction
		if err := decode.Decode(&action); err != nil || decode.Decode(new(any)) != io.EOF {
			http.Error(w, "无效的托盘操作", http.StatusBadRequest)
			return
		}
		if err := d.runPanelAction(p, action); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(nil)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (d *desktop) panelSnapshot(page string) map[string]any {
	status := d.core.Status()
	d.statusMu.RLock()
	state := "offline"
	var summary *tasks.Summary
	switch status.Phase {
	case "", "stopped":
		state = "stopped"
	case "starting":
		state = "loading"
	case "running":
		switch {
		case d.taskPID != status.PID || !d.taskAttempted:
			state = "loading"
		case d.taskSummary == nil:
			state = "unavailable"
		default:
			state, summary = "ready", d.taskSummary
		}
	}
	message, official := d.message, d.latestOfficial
	pendingApp := d.pendingApp
	pendingCore := ""
	if d.coreUpdate != nil {
		pendingCore = d.coreUpdate.DisplayVersion()
	}
	autostart := d.autostart
	d.statusMu.RUnlock()

	win, busy := d.window.Load(), d.busy.Load()
	result := map[string]any{"taskState": state, "canOpen": !busy || (win != nil && !win.paired),
		"availableHeight": panelHeight, "busy": busy}

	switch page {
	case "settings":
		result["shell"] = map[string]any{"autostart": autostart, "windowOpen": win != nil,
			"importOfficial": runtime.GOOS == "darwin"}
	case "about":
		current, _ := d.store.Current()
		coreVersion := ""
		if current != nil {
			coreVersion = current.Manifest.DisplayVersion()
			official = upstreamLabel(current.Manifest.Version, official)
		} else {
			official = upstreamLabel("", official)
		}
		result["shell"] = map[string]any{"appVersion": version, "bundledCore": d.seed != "",
			"coreVersion": coreVersion, "pendingCore": pendingCore, "officialVersion": official,
			"checkCore": d.cfg.UpdateRepository != "", "checkApp": d.appUpdateEnabled, "pendingApp": pendingApp}
	case "advanced":
		current, _ := d.store.Current()
		previous, _ := d.store.Previous()
		result["shell"] = map[string]any{"coreInstalled": current != nil || d.seed != "",
			"bundledCore": d.seed != "", "rollbackAvailable": previous != nil, "running": status.Phase == "running"}
	default:
		result["summary"] = summary
		return result
	}
	if chars := []rune(message); len(chars) > 180 {
		message = string(chars[:180])
	}
	result["message"] = message
	return result
}

func (d *desktop) runPanelAction(p *trayPanel, action panelAction) error {
	if d.quitting.Load() || d.panel.Load() != p {
		return errors.New("托盘面板已关闭")
	}
	switch action.Kind {
	case "resize":
		application.InvokeSync(func() {
			if d.panel.Load() != p {
				return
			}
			p.win.SetSize(panelWidth, max(80, min(panelHeight, action.Height)))
			_ = d.tray.PositionWindow(p.win, 5)
			if p.shown.CompareAndSwap(false, true) {
				p.win.Show().Focus()
			}
		})
	case "close":
		go d.closePanel(p)
	case "retry_tasks":
		d.statusMu.Lock()
		d.taskAttempted, d.taskSummary = false, nil
		d.statusMu.Unlock()
		select {
		case d.retryTasks <- struct{}{}:
		default:
		}
	case "open":
		status := d.core.Status()
		var target *tasks.Target
		if action.ID != "" {
			d.statusMu.RLock()
			if status.Phase == "running" && status.PID == d.taskPID && d.taskSummary != nil {
				for _, row := range d.taskSummary.Rows {
					if row.ID == action.ID {
						item := row.Target
						target = &item
						break
					}
				}
			}
			d.statusMu.RUnlock()
			if target == nil {
				return errors.New("任务状态已变化，请刷新后重试")
			}
		}
		if w := d.window.Load(); d.busy.Load() && (w == nil || w.paired) {
			return errors.New("Orca 正忙，请稍后再试")
		}
		go d.closePanel(p)
		d.request("open", target)
	case "open_paired", "close_orca", "import_official", "check_app", "install_app", "check_core", "install_core", "check_official",
		"restart", "stop", "rollback", "autostart":
		if d.busy.Load() && action.Kind != "close_orca" {
			return errors.New("Orca 正忙，请稍后再试")
		}
		if d.seed != "" && (action.Kind == "check_core" || action.Kind == "install_core" || action.Kind == "rollback") {
			return errors.New("此应用的内核随应用更新")
		}
		if action.Kind == "open_paired" {
			go d.closePanel(p)
		}
		d.request(action.Kind, nil)
	case "config", "logs", "data":
		return openExternal(map[string]string{"config": d.paths.settings, "logs": d.paths.logs, "data": d.paths.root}[action.Kind])
	case "quit":
		d.quit()
	default:
		return fmt.Errorf("未知托盘操作 %q", strings.ToValidUTF8(action.Kind, ""))
	}
	return nil
}

func upstreamLabel(current, latest string) string {
	if latest == "" {
		return ""
	}
	label := "官方最新 " + latest
	if !semver.IsValid(current) || !semver.IsValid(latest) {
		return label
	}
	switch semver.Compare(current, latest) {
	case 0:
		return label + " · 已同步"
	case 1:
		return label + " · 本机版本更高"
	}
	if semver.MajorMinor(current) == semver.MajorMinor(latest) && semver.Prerelease(current) == "" {
		patch := func(value string) int {
			parts := strings.Split(semver.Canonical(value), ".")
			n, _ := strconv.Atoi(parts[2])
			return n
		}
		if difference := patch(latest) - patch(current); difference > 0 {
			return fmt.Sprintf("%s · 低 %d 个补丁号", label, difference)
		}
	}
	return label + " · 有新版"
}

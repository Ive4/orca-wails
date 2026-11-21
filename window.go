package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"orca-wails/internal/tasks"
)

//go:embed assets/window-init.js
var themeScript string

//go:embed assets/pairing-init.js
var pairingScript string

//go:embed assets/bots-i18n.js
var botsScript string

//go:embed assets/bots-i18n.zh.json
var botsDictionary string

//go:embed assets/tray-reveal.js
var revealScript string

type orcaWindow struct {
	win           *application.WebviewWindow
	paired        bool
	ready         atomic.Bool
	page          *url.URL
	onReady       func()
	pendingReveal string
}

func validatedOffer(offer string, port uint16) (*url.URL, error) {
	u, err := url.Parse(offer)
	if err != nil || u.Scheme != "http" || u.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))) ||
		u.User != nil || u.Path != "/web-index.html" || u.RawQuery != "" {
		return nil, errors.New("内核提供了无效的本机 Web 地址")
	}
	return u, nil
}

func (d *desktop) showOrca(paired bool, target *tasks.Target, action string) error {
	offer, port, ok := d.core.Offer()
	if !ok {
		return errors.New("内核没有提供可用的配对信息，请查看日志或重启内核")
	}
	u, err := validatedOffer(offer, port)
	if err != nil {
		return err
	}
	title := "Orca"
	if paired {
		u.Host = net.JoinHostPort("localhost", strconv.Itoa(int(port)))
		u.RawQuery, u.Fragment, u.RawFragment = "", "", ""
		title = "Orca · 已配对服务器"
	}
	var reveal string
	if action == "open_bots" {
		reveal = "window.__orcaTrayOpenBots?.();"
	} else if target != nil && target.WorktreeID != "" && target.TabID != "" {
		value, err := json.Marshal(target)
		if err != nil {
			return err
		}
		reveal = "window.__orcaTrayReveal?.(" + string(value) + ");"
	}
	var windowErr error
	application.InvokeSync(func() {
		if d.quitting.Load() {
			return
		}
		if w := d.window.Load(); w != nil && w.paired == paired {
			if w.ready.Load() {
				w.win.UnMinimise()
				w.win.Show().Focus()
			}
			if reveal != "" {
				if w.ready.Load() {
					evalOrca(w.win, reveal)
				} else {
					w.pendingReveal = reveal
				}
			}
			return
		}
		d.closeOrca()
		host := u.Host
		originJSON, _ := json.Marshal("http://" + host)
		script := "(() => { if (window.top !== window || location.origin !== " + string(originJSON) + ") return;\n" +
			themeScript + "\n" + botsScript + "(\n" + botsDictionary + "\n);\n" + botAvatarScript()
		if !paired {
			fragment, _ := json.Marshal(u.EscapedFragment())
			script += pairingScript + "(" + string(fragment) + ");\n" + revealScript + "\n" + reveal + "\n"
		}
		script += "})();"

		w := &orcaWindow{paired: paired, page: u}
		options := application.WebviewWindowOptions{
			Name: "orca-main", Title: title, Width: 1280, Height: 840, MinWidth: 720, MinHeight: 480,
			URL: "about:blank", Hidden: true, Permissions: untrustedPermissions(),
			BackgroundType: application.BackgroundTypeSolid, BackgroundColour: application.NewRGB(24, 24, 24),
		}
		configureOrcaWindow(&options, script, u)
		w.win = d.app.Window.NewWithOptions(options)
		w.win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			if d.window.CompareAndSwap(w, nil) {
				setDockVisible(false)
			}
		})
		show := func() {
			application.InvokeSync(func() {
				if d.quitting.Load() || d.window.Load() != w || !w.ready.CompareAndSwap(false, true) {
					return
				}
				setDockVisible(true)
				w.win.Show().Focus()
				go d.refreshNativeTray(false)
				if w.pendingReveal != "" {
					evalOrca(w.win, w.pendingReveal)
					w.pendingReveal = ""
				}
			})
		}
		w.onReady = show
		d.window.Store(w)
		if err := loadOrcaWindow(w.win, u, script, show); err != nil {
			d.closeOrca()
			windowErr = err
			return
		}
		time.AfterFunc(30*time.Second, func() {
			application.InvokeSync(func() {
				if d.window.Load() == w && !w.ready.Load() {
					d.closeOrca()
					d.setMessage("Orca 窗口加载超时")
					d.showNativeManagement()
					go d.refreshNativeTray(false)
				}
			})
		})
	})
	return windowErr
}

func (d *desktop) closeOrca() *orcaWindow {
	var result *orcaWindow
	application.InvokeSync(func() {
		result = d.window.Swap(nil)
		if result != nil {
			result.win.Close()
			setDockVisible(false)
		}
	})
	return result
}

func untrustedPermissions() map[application.PermissionType]application.Permission {
	return map[application.PermissionType]application.Permission{
		application.PermissionMicrophone:    application.PermissionDeny,
		application.PermissionCamera:        application.PermissionDeny,
		application.PermissionGeolocation:   application.PermissionDeny,
		application.PermissionNotifications: application.PermissionDeny,
		application.PermissionClipboardRead: application.PermissionDeny,
	}
}

func browserUnavailable() error {
	return fmt.Errorf("无法初始化 Orca WebView")
}

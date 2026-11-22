package main

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"orca-wails/internal/core"
	"orca-wails/internal/tasks"
)

const desktopIndex = "/desktop/index.html"

//go:embed assets/window-init.js
var themeScript string

//go:embed assets/pairing-init.js
var pairingScript string

//go:embed assets/desktop-init.js
var desktopScript string

//go:embed assets/bots-i18n.js
var botsScript string

//go:embed assets/bots-i18n.zh.json
var botsDictionary string

//go:embed assets/tray-reveal.js
var revealScript string

type legacyDesktopStorage struct {
	Port           uint16 `json:"-"`
	InstallationID string `json:"installationId"`
	PublicKey      string `json:"publicKeyB64"`
}

func (d *desktop) legacyDesktopStorage(port uint16) legacyDesktopStorage {
	legacy := legacyDesktopStorage{Port: port}
	read := func(name string, value any) {
		file, err := os.Open(filepath.Join(d.paths.data, name))
		if err == nil {
			defer file.Close()
			_ = json.NewDecoder(io.LimitReader(file, 4<<10)).Decode(value)
		}
	}
	read("host-installation.json", &legacy)
	read("orca-e2ee-keypair.json", &legacy)
	return legacy
}

type orcaWindow struct {
	win                *application.WebviewWindow
	paired             bool
	ready              atomic.Bool
	page               *url.URL
	onReady            func()
	pendingReveal      string
	desktopDir         string
	shellSettings      bool
	desktopToken       string
	desktopStreams     sync.Map
	desktopStreamCount atomic.Int32
	desktopContext     context.Context
	desktopCancel      context.CancelFunc
}

func validatedOffer(offer string, port uint16) (*url.URL, error) {
	u, err := url.Parse(offer)
	if err != nil || u.Scheme != "http" || u.Host != net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))) ||
		u.User != nil || u.Path != "/web-index.html" || u.RawQuery != "" {
		return nil, errors.New("内核提供了无效的本机 Web 地址")
	}
	return u, nil
}

func orcaWindowPage(status core.Status, offer string, desktopUI, paired bool) (*url.URL, error) {
	if status.Phase != "running" || status.Port == 0 {
		return nil, errors.New("Orca 内核尚未就绪")
	}
	if paired {
		return &url.URL{Scheme: "http", Host: net.JoinHostPort("localhost", strconv.Itoa(int(status.Port))), Path: "/web-index.html"}, nil
	}
	if desktopUI {
		return &url.URL{Path: desktopIndex}, nil
	}
	if offer == "" {
		return nil, errors.New("旧版内核没有提供启动配对信息")
	}
	return validatedOffer(offer, status.Port)
}

func (d *desktop) showOrca(paired bool, target *tasks.Target, action string) error {
	slot, err := d.store.Current()
	if err != nil {
		return err
	}
	shellSettings := slot != nil && slot.Manifest.DesktopProtocol == 1 && slot.Manifest.ShellSettings &&
		runtime.GOOS == "darwin" && d.nativeTray
	if action == "open_shell_settings" && !shellSettings {
		d.showNativeManagement()
		return nil
	}
	dir := ""
	if slot != nil && !paired && slot.Manifest.DesktopProtocol == 1 {
		dir = filepath.Join(slot.Dir, "web")
		entry, statErr := os.Stat(filepath.Join(dir, "wails-index.html"))
		if statErr != nil || !entry.Mode().IsRegular() {
			return errors.New("Orca 内核缺少本机桌面入口")
		}
	}
	offer, _, _ := d.core.Offer()
	status := d.core.Status()
	u, err := orcaWindowPage(status, offer, dir != "", paired)
	if err != nil {
		return err
	}
	if dir != "" {
		if runtime.GOOS == "windows" {
			u.Scheme, u.Host = "http", "wails.localhost"
		} else {
			u.Scheme, u.Host = "wails", "localhost"
		}
	}
	title := "Orca"
	if paired {
		title = "Orca · 已配对服务器"
	}
	var reveal string
	if action == "open_bots" {
		reveal = "window.__orcaTrayOpenBots?.();"
	} else if action == "open_shell_settings" {
		reveal = "window.__orcaWailsDesktop?.openSettings();"
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
		w := &orcaWindow{paired: paired, page: u, desktopDir: dir, shellSettings: shellSettings && !paired}
		originJSON, _ := json.Marshal(u.Scheme + "://" + u.Host)
		pathJSON, _ := json.Marshal(u.Path)
		script := "(() => { if (window.top !== window || location.protocol + '//' + location.host !== " +
			string(originJSON) + " || location.pathname !== " + string(pathJSON) + ") return;\n" +
			themeScript + "\n" + botsScript + "(\n" + botsDictionary + "\n);\n" + botAvatarScript()
		if !paired {
			if dir != "" {
				w.desktopContext, w.desktopCancel = context.WithCancel(d.ctx)
				w.desktopToken = hex.EncodeToString(randomToken())
				options, _ := json.Marshal(map[string]any{
					"token": w.desktopToken, "platform": runtime.GOOS, "shellSettings": w.shellSettings,
				})
				script += desktopScript + "(" + string(options) + ");\n"
			} else {
				fragment, _ := json.Marshal(u.EscapedFragment())
				script += pairingScript + "(" + string(fragment) + ");\n"
			}
			script += revealScript + "\n" + reveal + "\n"
		}
		script += "})();"

		options := application.WebviewWindowOptions{
			Name: "orca-main", Title: title, Width: 1280, Height: 840, MinWidth: 720, MinHeight: 480,
			URL: "about:blank", Hidden: true, Permissions: untrustedPermissions(),
			BackgroundType: application.BackgroundTypeSolid, BackgroundColour: application.NewRGB(24, 24, 24),
		}
		configureOrcaWindow(&options, script, u)
		w.win = d.app.Window.NewWithOptions(options)
		w.win.OnWindowEvent(events.Common.WindowClosing, func(*application.WindowEvent) {
			w.cancelDesktop()
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
		var legacy legacyDesktopStorage
		if !paired && runtime.GOOS == "darwin" && dir != "" {
			legacy = d.legacyDesktopStorage(status.Port)
		}
		if err := loadOrcaWindow(w.win, u, script, legacy, show); err != nil {
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
			result.cancelDesktop()
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

package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"orca-wails/internal/core"
	"orca-wails/internal/tasks"
	"orca-wails/internal/update"
)

const appName = "Orca-Wails"

var (
	version    = "0.1.0"
	instanceID = "dev.orca-wails.desktop"
)

//go:embed build/128x128.png
var appIcon []byte

type desktop struct {
	app  *application.App
	tray *application.SystemTray

	ctx    context.Context
	cancel context.CancelFunc
	cfg    config
	paths  paths
	core   core.Manager
	store  update.Store
	seed   string
	launch launchOptions

	operation sync.Mutex
	busy      atomic.Bool
	pending   atomic.Bool
	quitting  atomic.Bool
	quitReady atomic.Bool

	statusMu         sync.RWMutex
	message          string
	latestOfficial   string
	pendingApp       string
	appUpdateEnabled bool
	coreUpdate       *update.Release
	taskPID          int
	taskSummary      *tasks.Summary
	taskAttempted    bool
	retryTasks       chan struct{}
	autostart        bool

	window           atomic.Pointer[orcaWindow]
	panel            atomic.Pointer[trayPanel]
	panelDismissedAt atomic.Int64
}

func main() {
	background := flag.Bool("background", false, "start without a webview")
	dataDir := flag.String("data-dir", "", "absolute app data directory")
	bootstrap := flag.String("bootstrap", "", "path to a core archive with adjacent bootstrap identity JSON")
	flag.Parse()
	opts := launchOptions{Background: *background, DataDir: *dataDir, Bootstrap: *bootstrap}
	if opts.DataDir == "" {
		opts.DataDir = os.Getenv("ORCA_WAILS_HOME")
	}
	if opts.Bootstrap == "" {
		opts.Bootstrap = os.Getenv("ORCA_WAILS_BOOTSTRAP")
	}
	if restored, ok := readRelaunch(); ok && opts == (launchOptions{}) && flag.NFlag() == 0 {
		opts = restored
	}
	if err := run(opts); err != nil {
		fmt.Fprintln(os.Stderr, appName+":", err)
		os.Exit(1)
	}
}

func run(opts launchOptions) error {
	root := opts.DataDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".orca-wails")
	}
	if !filepath.IsAbs(root) {
		return errors.New("data directory must be absolute")
	}
	root = filepath.Clean(root)
	bootstrap := opts.Bootstrap
	if bootstrap == "" {
		bootstrap = bundledArchive()
	} else {
		var err error
		bootstrap, err = filepath.Abs(bootstrap)
		if err != nil {
			return err
		}
		opts.Bootstrap = bootstrap
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &desktop{
		ctx: ctx, cancel: cancel, seed: bootstrap, launch: opts, retryTasks: make(chan struct{}, 1),
	}
	d.app = application.New(application.Options{
		Name: appName, Description: "独立 Orca Core 的桌面宿主", Icon: appIcon,
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		Windows: application.WindowsOptions{
			DisableQuitOnLastWindowClosed: true,
			WebviewUserDataPath:           filepath.Join(root, "webview"),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: instanceID,
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				for _, arg := range data.Args {
					if arg == "--background" || arg == "-background" {
						return
					}
				}
				d.request("open", nil)
			},
		},
		Assets: application.AssetOptions{
			Handler: http.HandlerFunc(d.serveAssets), DisableLogging: true,
			Middleware: func(http.Handler) http.Handler { return http.HandlerFunc(d.serveAssets) },
		},
		DisableDefaultSignalHandler: true,
		RawMessageHandler:           d.handleWindowMessage,
		Logger:                      slog.New(slog.NewTextHandler(io.Discard, nil)),
		ShouldQuit: func() bool {
			if d.quitReady.Load() {
				return true
			}
			d.quit()
			return false
		},
		OnShutdown: func() {
			d.cancel()
			if !d.quitReady.Load() {
				d.core.EmergencyStop()
			}
		},
	})
	var err error
	d.cfg, d.paths, err = loadConfig(root)
	if err != nil {
		return err
	}
	d.autostart = autostartEnabled()
	if err := d.configureAppUpdater(); err != nil {
		return err
	}
	d.store = update.Store{Root: d.paths.core, Repository: d.cfg.UpdateRepository,
		Proxy: d.cfg.DownloadProxy, Target: targetTriple()}
	d.configureTray()
	d.app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go d.start(opts.Background)
	})
	if runtime.GOOS == "darwin" {
		d.app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(_ *application.ApplicationEvent) {
			d.request("open", nil)
		})
		d.configureMenu()
	}
	return d.app.Run()
}

func (d *desktop) start(background bool) {
	shortcut := strings.ReplaceAll(strings.ToLower(d.cfg.GlobalShortcut), "commandorcontrol", "cmdorctrl")
	if err := d.app.GlobalShortcut.Register(shortcut, func() { d.request("toggle", nil) }); err != nil {
		d.setMessage("全局快捷键不可用：" + err.Error())
	}
	signals, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		defer stopSignals()
		select {
		case <-signals.Done():
			d.quit()
		case <-d.ctx.Done():
		}
	}()
	go d.pollTasks()
	if !background && d.cfg.OpenWindowOnStart {
		d.request("open", nil)
	} else if d.cfg.StartCoreOnLaunch {
		d.request("start", nil)
	}
}

func bundledArchive() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for _, candidate := range []string{
		filepath.Join(dir, "..", "Resources", "bootstrap-core.zip"),
		filepath.Join(dir, "bootstrap-core.zip"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

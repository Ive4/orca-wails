package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"time"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"github.com/wailsapp/wails/v3/pkg/updater/providers/endpoint"
)

// Release builds set this to the base64-encoded Ed25519 public key.
var appUpdatePublicKey string

type httpsOnlyTransport struct {
	http.RoundTripper
}

func (t httpsOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" || req.URL.User != nil {
		return nil, errors.New("应用更新必须通过 HTTPS 下载")
	}
	return t.RoundTripper.RoundTrip(req)
}

func (d *desktop) configureAppUpdater() error {
	if d.cfg.UpdateRepository == "" || appUpdatePublicKey == "" ||
		(runtime.GOOS != "darwin" && runtime.GOOS != "windows") {
		return nil
	}
	key, err := base64.StdEncoding.DecodeString(appUpdatePublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("invalid application update public key")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if d.cfg.DownloadProxy != "" {
		proxy, err := url.Parse(d.cfg.DownloadProxy)
		if err != nil {
			return err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{
		Transport: httpsOnlyTransport{transport}, Timeout: 3 * time.Minute,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || len(via) > 8 {
				return errors.New("invalid application update redirect")
			}
			return nil
		},
	}
	provider, err := endpoint.New(endpoint.Config{
		URL:        "https://github.com/" + d.cfg.UpdateRepository + "/releases/latest/download/wails-update.json",
		HTTPClient: client,
	})
	if err != nil {
		return err
	}
	err = d.app.Updater.Init(updater.Config{
		CurrentVersion: version, Providers: []updater.Provider{provider}, PublicKey: key,
		Window: updater.WindowNone,
	})
	d.appUpdateEnabled = err == nil
	return err
}

func (d *desktop) checkAppUpdate() error {
	if !d.appUpdateEnabled {
		return errors.New("未配置应用签名更新")
	}
	d.statusMu.Lock()
	d.pendingApp = ""
	d.message = "正在检查应用更新…"
	d.statusMu.Unlock()
	release, err := d.app.Updater.Check(d.ctx)
	if err != nil {
		return err
	}
	if release == nil {
		d.setMessage("当前应用已是最新版本")
		return nil
	}
	d.statusMu.Lock()
	d.pendingApp = release.Version
	d.message = "发现应用更新 · " + release.Version
	d.statusMu.Unlock()
	return nil
}

func (d *desktop) installAppUpdate() error {
	if !d.appUpdateEnabled {
		return errors.New("未配置应用签名更新")
	}
	d.statusMu.RLock()
	nextVersion := d.pendingApp
	d.statusMu.RUnlock()
	if nextVersion == "" {
		return errors.New("请先检查应用更新")
	}
	d.setMessage("正在下载和验证应用更新…")
	if err := d.app.Updater.DownloadAndInstall(d.ctx); err != nil {
		return err
	}
	if err := d.ctx.Err(); err != nil {
		return err
	}
	marker, err := prepareRelaunch(nextVersion, d.launch)
	if err != nil {
		return fmt.Errorf("保存重启参数: %w", err)
	}
	var handedOff bool
	defer func() {
		if marker != "" && !handedOff {
			_ = os.Remove(marker)
		}
	}()
	d.setMessage("正在安装应用并重启…")
	wasRunning := d.core.Status().Phase == "running"
	reopen := d.closeOrca()
	if err := d.core.Stop(d.ctx); err != nil {
		return err
	}
	if !d.quitting.CompareAndSwap(false, true) {
		return context.Canceled
	}
	d.quitReady.Store(true)
	if err := d.app.Updater.Restart(d.ctx); err != nil {
		d.quitReady.Store(false)
		d.quitting.Store(false)
		return errors.Join(fmt.Errorf("安装应用更新: %w", err), d.restoreImport(reopen, wasRunning))
	}
	handedOff = true
	return nil
}

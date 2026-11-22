package main

import (
	"encoding/json"
	"net/url"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func configureOrcaWindow(options *application.WebviewWindowOptions, script string, page *url.URL) {
	// Wails registers its document-start JS with WebView2 in the HTML launch path.
	options.HTML = "<!doctype html><title>Orca</title>"
	origin, _ := json.Marshal(page.Scheme + "://" + page.Host)
	path, _ := json.Marshal(page.Path)
	options.JS = script + `
(() => {
  if (window.top !== window || location.origin !== ` + string(origin) + ` || location.pathname !== ` + string(path) + `) return;
  window.navigation?.addEventListener("navigate", event => {
    const next = new URL(event.destination.url);
    if (next.origin !== location.origin && event.cancelable) {
      event.preventDefault();
      if (event.userInitiated && (next.protocol === "http:" || next.protocol === "https:") && !next.username && !next.password) {
        window.open(next.href, "_blank", "noopener,noreferrer");
      }
    }
  });
  window.addEventListener("load", () => window.chrome.webview.postMessage("orca:page-ready"), {once: true});
})();
`
}

func loadOrcaWindow(window *application.WebviewWindow, page *url.URL, _ string, _ legacyDesktopStorage, _ func()) error {
	var once sync.Once
	window.OnWindowEvent(events.Windows.WebViewNavigationCompleted, func(*application.WindowEvent) {
		once.Do(func() { window.SetURL(page.String()) })
	})
	return nil
}

func (d *desktop) handleWindowMessage(window application.Window, message string, info *application.OriginInfo) {
	if message != "orca:page-ready" || info == nil || info.TopOrigin != info.Origin {
		return
	}
	current := d.window.Load()
	if current == nil || current.win.ID() != window.ID() || current.ready.Load() {
		return
	}
	source, err := url.Parse(info.Origin)
	if err != nil || source.User != nil || source.Scheme != current.page.Scheme ||
		source.Host != current.page.Host || source.Path != current.page.Path {
		return
	}
	current.win.HandleMessage("wails:runtime:ready")
	current.onReady()
}

func evalOrca(window *application.WebviewWindow, script string) { window.ExecJS(script) }

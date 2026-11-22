//go:build !darwin && !windows

package main

import (
	"net/url"

	"github.com/wailsapp/wails/v3/pkg/application"
)

func configureOrcaWindow(options *application.WebviewWindowOptions, script string, _ *url.URL) {
	options.JS = script
}

func loadOrcaWindow(window *application.WebviewWindow, page *url.URL, _ string, _ legacyDesktopStorage, show func()) error {
	window.SetURL(page.String())
	show()
	return nil
}

func evalOrca(window *application.WebviewWindow, script string) { window.ExecJS(script) }

func (d *desktop) handleWindowMessage(application.Window, string, *application.OriginInfo) {}

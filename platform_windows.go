package main

import (
	"context"
	"sync"
	"time"

	"errors"
	"fmt"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/services/notifications"
	"os"
	"runtime"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const windowsRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func setAutostart(enabled bool) error {
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(autostartID)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command := `"` + exe + `" ` + backgroundArg
	if len(utf16.Encode([]rune(command))) > 260 {
		return errors.New("autostart command exceeds the Windows Run entry limit of 260 characters")
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue(autostartID, command)
}

func autostartEnabled() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetStringValue(autostartID)
	return err == nil && value != ""
}

func openExternal(target string) error {
	target, err := externalTarget(target)
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	// ShellExecute can invoke COM-based shell extensions on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	switch err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err {
	case nil, syscall.Errno(windows.S_FALSE):
		defer windows.CoUninitialize()
	case syscall.Errno(windows.RPC_E_CHANGED_MODE):
		// The caller already has a COM apartment.
	default:
		return fmt.Errorf("initialize COM for ShellExecute: %w", err)
	}
	return windows.ShellExecute(0, nil, file, nil, nil, windows.SW_SHOWNORMAL)
}

func setDockVisible(bool) {}

var (
	notifierOnce sync.Once
	notifier     *notifications.NotificationService
)

func notifyDesktop(message string) {
	application.InvokeSync(func() {
		notifierOnce.Do(func() {
			service := notifications.New()
			if err := service.ServiceStartup(context.Background(), application.ServiceOptions{}); err == nil {
				notifier = service
				application.Get().OnShutdown(func() { _ = service.ServiceShutdown() })
			}
		})
		if notifier != nil {
			_ = notifier.SendNotification(notifications.NotificationOptions{
				ID: fmt.Sprint(time.Now().UnixNano()), Title: appName, Body: message,
			})
		}
	})
}

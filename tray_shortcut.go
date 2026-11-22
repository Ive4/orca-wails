package main

import (
	"errors"
	"strings"
)

type trayShortcutRegistrar interface {
	Register(string, func()) error
	IsRegistered(string) bool
	Unregister(string) error
}

func trayShortcutName(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "commandorcontrol", "cmdorctrl")
}

func replaceTrayShortcut(registrar trayShortcutRegistrar, previous, next string, toggle func(), save func() error) error {
	oldKey, newKey := trayShortcutName(previous), trayShortcutName(next)
	if registrar.IsRegistered(oldKey) && registrar.IsRegistered(newKey) {
		return save()
	}
	if err := registrar.Register(newKey, toggle); err != nil {
		return err
	}
	if err := save(); err != nil {
		return errors.Join(err, registrar.Unregister(newKey))
	}
	if oldKey != newKey && registrar.IsRegistered(oldKey) {
		return registrar.Unregister(oldKey)
	}
	return nil
}

func (d *desktop) setTrayShortcut(value string) error {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 {
		return errors.New("请输入打开 Orca 的按键组合")
	}
	d.settingsMu.Lock()
	defer d.settingsMu.Unlock()
	cfg, _, err := loadConfig(d.paths.root)
	if err != nil {
		return err
	}
	cfg.GlobalShortcut = value
	return replaceTrayShortcut(d.app.GlobalShortcut, d.cfg.GlobalShortcut, value,
		func() { d.request("toggle", nil) },
		func() error {
			if err := saveConfig(cfg, d.paths.settings); err != nil {
				return err
			}
			d.statusMu.Lock()
			d.cfg.GlobalShortcut = value
			d.statusMu.Unlock()
			return nil
		})
}

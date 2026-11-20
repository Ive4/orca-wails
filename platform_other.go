//go:build !windows && (!darwin || ios)

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

func setAutostart(bool) error { return fmt.Errorf("autostart: %w", errors.ErrUnsupported) }

func autostartEnabled() bool { return false }

func openExternal(target string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("open external target: %w", errors.ErrUnsupported)
	}
	target, err := externalTarget(target)
	if err != nil {
		return err
	}
	return exec.Command("xdg-open", target).Run()
}

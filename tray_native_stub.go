//go:build !darwin || !cgo || !swifttray

package main

import "encoding/json"

func nativeShellSettings(string, string) json.RawMessage { return nil }

func (*desktop) showNativeManagement() {}

func (*desktop) startNativeTray() bool                       { return false }
func (*desktop) sendNativeSnapshot(map[string]any)           {}
func (*desktop) sendNativeInteraction(trayInteractionResult) {}
func (*desktop) sendNativeNotice(trayNotice)                 {}
func (*desktop) sendNativeTaskSounds([]string)               {}
func (*desktop) clearNativeNotices([]string)                 {}

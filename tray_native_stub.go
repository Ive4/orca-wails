//go:build !darwin || !cgo || !swifttray

package main

func (*desktop) showNativeManagement() {}

func (*desktop) startNativeTray() bool                       { return false }
func (*desktop) sendNativeSnapshot(map[string]any)           {}
func (*desktop) sendNativeInteraction(trayInteractionResult) {}
func (*desktop) sendNativeNotice(trayNotice)                 {}
func (*desktop) clearNativeNotices([]string)                 {}

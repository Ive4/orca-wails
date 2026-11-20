//go:build !windows && (!darwin || ios || !cgo)

package main

func setDockVisible(bool)  {}
func notifyDesktop(string) {}

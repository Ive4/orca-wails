//go:build !darwin || ios || !cgo

package main

func readMacTrayDisplay() macTrayDisplay { return macTrayDisplay{reduceMotion: true} }

package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const (
	autostartID   = "dev.orca-wails.desktop"
	backgroundArg = "--background"
)

// externalTarget accepts HTTP(S) URLs and existing filesystem paths. Absolute
// paths also keep a relative filename from being interpreted as a shell option.
func externalTarget(target string) (string, error) {
	if strings.TrimSpace(target) == "" || strings.ContainsRune(target, 0) {
		return "", errors.New("invalid external target")
	}
	if !filepath.IsAbs(target) {
		parsed, err := url.Parse(target)
		if err == nil && parsed.IsAbs() {
			if (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" {
				return target, nil
			}
			return "", errors.New("external URL must use HTTP or HTTPS and have a host")
		}
	}
	path, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

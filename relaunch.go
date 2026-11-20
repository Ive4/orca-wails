package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type launchOptions struct {
	DataDir    string
	Bootstrap  string
	Background bool
}

type relaunchState struct {
	Version string
	Expires time.Time
	Options launchOptions
}

func relaunchPath() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "orca-wails", instanceID+"-relaunch.json"), nil
}

func readRelaunch() (launchOptions, bool) {
	path, err := relaunchPath()
	if err != nil {
		return launchOptions{}, false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8<<10 {
		return launchOptions{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return launchOptions{}, false
	}
	var state relaunchState
	err = json.NewDecoder(io.LimitReader(file, 8<<10)).Decode(&state)
	file.Close()
	if err != nil || time.Now().After(state.Expires) {
		_ = os.Remove(path)
		return launchOptions{}, false
	}
	if state.Version != version {
		return launchOptions{}, false
	}
	_ = os.Remove(path)
	return state.Options, true
}

func prepareRelaunch(nextVersion string, options launchOptions) (string, error) {
	if options == (launchOptions{}) {
		return "", nil
	}
	path, err := relaunchPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".relaunch-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(relaunchState{
		Version: nextVersion, Options: options, Expires: time.Now().Add(5 * time.Minute),
	})
	if err := errors.Join(err, file.Close()); err != nil {
		return "", err
	}
	return path, os.Rename(file.Name(), path)
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"orca-wails/internal/core"
	"orca-wails/internal/update"
)

type config struct {
	Port                uint16            `json:"port"`
	CoreDirectory       string            `json:"core_directory"`
	UpdateRepository    string            `json:"update_repository"`
	DownloadProxy       string            `json:"download_proxy"`
	StartCoreOnLaunch   bool              `json:"start_core_on_launch"`
	OpenWindowOnStart   bool              `json:"open_window_on_start"`
	Autostart           bool              `json:"autostart"`
	GlobalShortcut      string            `json:"global_shortcut"`
	Notifications       bool              `json:"notifications"`
	MacOSEntries        macOSEntries      `json:"macos_entries"`
	Environment         map[string]string `json:"environment"`
	StartupTimeoutSecs  int               `json:"startup_timeout_secs"`
	ShutdownTimeoutSecs int               `json:"shutdown_timeout_secs"`
}

type macOSEntries struct {
	MenuBar bool `json:"menu_bar"`
	Notch   bool `json:"notch"`
}

type paths struct {
	root, settings, core, data, logs, webview string
}

func loadConfig(root string) (config, paths, error) {
	cfg := config{
		Port: 6768, StartCoreOnLaunch: true, OpenWindowOnStart: true,
		GlobalShortcut: "CommandOrControl+Shift+O", Notifications: true,
		MacOSEntries: macOSEntries{MenuBar: true},
		Environment:  map[string]string{}, StartupTimeoutSecs: 60, ShutdownTimeoutSecs: 8,
	}
	if runtime.GOOS == "darwin" {
		cfg.Port = 16768
	}
	p := paths{root: root, settings: filepath.Join(root, "config.json"), core: filepath.Join(root, "core"),
		data: filepath.Join(root, "orca-data"), logs: filepath.Join(root, "logs"), webview: filepath.Join(root, "webview")}
	if err := os.MkdirAll(root, 0700); err != nil {
		return cfg, p, err
	}
	f, err := os.Open(p.settings)
	if err == nil {
		defer f.Close()
		const limit = 64 << 10
		data, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return cfg, p, err
		}
		if len(data) > limit || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
			return cfg, p, errors.New("config.json must be an object under 64 KiB")
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&cfg); err != nil {
			return cfg, p, fmt.Errorf("parse config.json: %w", err)
		}
		if decoder.Decode(new(any)) != io.EOF {
			return cfg, p, errors.New("unexpected trailing content in config.json")
		}
	} else if !os.IsNotExist(err) {
		return cfg, p, err
	} else if err := saveConfig(cfg, p.settings); err != nil {
		return cfg, p, err
	}

	if err := cfg.validate(); err != nil {
		return cfg, p, err
	}
	if cfg.CoreDirectory != "" {
		p.core = cfg.CoreDirectory
	}
	for _, dir := range []string{p.core, p.data, p.logs, p.webview} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return cfg, p, err
		}
	}
	return cfg, p, nil
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

func (c config) validate() error {
	if c.Port < 1024 || c.StartupTimeoutSecs < 5 || c.StartupTimeoutSecs > 300 ||
		c.ShutdownTimeoutSecs < 1 || c.ShutdownTimeoutSecs > 30 {
		return errors.New("invalid port or core timeout in config.json")
	}
	if strings.TrimSpace(c.GlobalShortcut) == "" {
		return errors.New("global_shortcut cannot be empty")
	}
	if c.CoreDirectory != "" && !filepath.IsAbs(c.CoreDirectory) {
		return errors.New("core_directory must be absolute")
	}
	if c.UpdateRepository != "" {
		if !repoName.MatchString(c.UpdateRepository) {
			return errors.New("update_repository must be owner/repository")
		}
		for _, part := range strings.Split(c.UpdateRepository, "/") {
			if part == "." || part == ".." {
				return errors.New("invalid update_repository")
			}
		}
	}
	if c.DownloadProxy != "" {
		u, err := url.Parse(c.DownloadProxy)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("download_proxy must use HTTP or HTTPS")
		}
	}
	for key, value := range c.Environment {
		switch strings.ToUpper(key) {
		case "", "ORCA_USER_DATA", "ORCA_VERSION":
			return errors.New("invalid or reserved environment variable")
		}
		if strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment entry")
		}
	}
	return nil
}

func saveConfig(c config, path string) error {
	if err := c.validate(); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	enc := json.NewEncoder(file)
	enc.SetIndent("", "  ")
	err = enc.Encode(c)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (c config) launchSpec(p paths, slot *update.Installed) (core.Spec, error) {
	dir, err := filepath.EvalSymlinks(slot.Dir)
	if err != nil {
		return core.Spec{}, err
	}
	m := slot.Manifest
	program, err := packagePath(dir, m.Executable, false)
	if err != nil {
		return core.Spec{}, err
	}
	cwd, err := packagePath(dir, m.WorkingDirectory, true)
	if err != nil {
		return core.Spec{}, err
	}
	port := strconv.Itoa(int(c.Port))
	args := make([]string, len(m.Args))
	values := map[string]string{"host": "127.0.0.1", "port": port, "core_dir": dir, "data_dir": p.data}
	for i, arg := range m.Args {
		if args[i], err = expand(arg, values); err != nil {
			return core.Spec{}, err
		}
	}
	env := make(map[string]string, len(m.Environment)+len(c.Environment)+2)
	for key, value := range m.Environment {
		if env[key], err = expand(value, values); err != nil {
			return core.Spec{}, err
		}
	}
	for key, value := range c.Environment {
		if runtime.GOOS == "windows" {
			for existing := range env {
				if strings.EqualFold(existing, key) {
					delete(env, existing)
				}
			}
		}
		env[key] = value
	}
	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	for key, value := range env {
		if strings.EqualFold(key, "NO_PROXY") {
			noProxy = value
			delete(env, key)
		}
	}
	var exclusions []string
	seen := map[string]bool{}
	for _, part := range strings.Split(noProxy+",localhost,127.0.0.1,::1", ",") {
		part = strings.TrimSpace(part)
		if part != "" && !seen[strings.ToLower(part)] {
			exclusions = append(exclusions, part)
			seen[strings.ToLower(part)] = true
		}
	}
	env["NO_PROXY"] = strings.Join(exclusions, ",")
	if runtime.GOOS != "windows" {
		env["no_proxy"] = env["NO_PROXY"]
	}
	if !strings.HasPrefix(m.HealthPath, "/") || strings.HasPrefix(m.HealthPath, "//") || strings.ContainsAny(m.HealthPath, "\\?#") {
		return core.Spec{}, errors.New("invalid core health path")
	}
	health := url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", port), Path: m.HealthPath}
	return core.Spec{Program: program, Args: args, Dir: cwd, Env: env, Port: c.Port,
		HealthURL: health.String(), LogDir: p.logs, Version: m.RuntimeVersion(),
		StartupTimeout:  time.Duration(c.StartupTimeoutSecs) * time.Second,
		ShutdownTimeout: time.Duration(c.ShutdownTimeoutSecs) * time.Second}, nil
}

func packagePath(dir, name string, directory bool) (string, error) {
	path, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("core path escapes package")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return "", errors.New("unexpected core path type")
	}
	return path, nil
}

func expand(template string, values map[string]string) (string, error) {
	var out strings.Builder
	for {
		before, after, found := strings.Cut(template, "{")
		out.WriteString(before)
		if !found {
			return out.String(), nil
		}
		key, rest, closed := strings.Cut(after, "}")
		value, known := values[key]
		if !closed || !known {
			return "", errors.New("invalid core manifest placeholder")
		}
		out.WriteString(value)
		template = rest
	}
}

func targetTriple() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "darwin/arm64":
		return "aarch64-apple-darwin"
	case "windows/amd64":
		return "x86_64-pc-windows-msvc"
	default:
		return runtime.GOARCH + "-" + runtime.GOOS
	}
}

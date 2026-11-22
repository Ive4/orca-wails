package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"orca-wails/internal/update"
)

func TestLoadConfigDoesNotReplaceInvalidFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	const invalid = `{"port":16768,"unrecognized":true}`
	if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(root); err == nil {
		t.Fatal("accepted unknown configuration field")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != invalid {
		t.Fatalf("invalid config changed: %q (%v)", data, err)
	}
}

func TestExpandDoesNotInterpretBracesInInsertedPaths(t *testing.T) {
	got, err := expand("{core_dir}/server.js", map[string]string{"core_dir": "/data/path {port}"})
	if err != nil || got != "/data/path {port}/server.js" {
		t.Fatalf("unexpected expansion %q (%v)", got, err)
	}
}

func TestMacOSEntryPreferencesAreIndependentAndPersisted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"notifications":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, paths, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MacOSEntries.MenuBar || cfg.MacOSEntries.Notch {
		t.Fatalf("legacy defaults: %+v", cfg.MacOSEntries)
	}
	d := desktop{paths: paths, entries: cfg.MacOSEntries}
	for _, step := range []struct {
		action string
		want   macOSEntries
	}{
		{"notch_on", macOSEntries{MenuBar: true, Notch: true}},
		{"menu_bar_off", macOSEntries{MenuBar: false, Notch: true}},
		{"notch_off", macOSEntries{MenuBar: false, Notch: false}},
		{"menu_bar_on", macOSEntries{MenuBar: true, Notch: false}},
	} {
		if err := d.setMacOSEntry(step.action); err != nil {
			t.Fatalf("%s: %v", step.action, err)
		}
		saved, _, err := loadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if saved.MacOSEntries != step.want || d.entries != step.want || saved.Notifications {
			t.Fatalf("%s: saved %+v, active %+v, want %+v", step.action, saved.MacOSEntries, d.entries, step.want)
		}
	}
}

func TestComputerUsePreferencesArePersisted(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"notifications":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, paths, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := "orca"
	if runtime.GOOS == "darwin" {
		wantDefault = "cua"
	}
	if cfg.Backend != wantDefault || cfg.VMSSH != "" {
		t.Fatalf("legacy defaults: %+v", cfg.computerUseSettings)
	}
	d := desktop{paths: paths, computerUse: cfg.computerUseSettings}
	for _, want := range []computerUseSettings{
		{Backend: "cua-vm", VMSSH: "orca-lume"},
		{Backend: "orca", VMSSH: "orca-lume"},
		{Backend: "cua", VMSSH: "orca-lume"},
	} {
		if err := d.setComputerUse(want); err != nil {
			t.Fatal(err)
		}
		saved, _, err := loadConfig(root)
		if err != nil {
			t.Fatal(err)
		}
		if saved.computerUseSettings != want || d.computerUse != want || saved.Notifications {
			t.Fatalf("saved %+v, active %+v, want %+v", saved.computerUseSettings, d.computerUse, want)
		}
	}
	savedBefore := d.computerUse
	for _, invalid := range []computerUseSettings{
		{Backend: "unknown"},
		{Backend: "cua-vm"},
		{Backend: "cua-vm", VMSSH: "-F"},
		{Backend: "cua-vm", VMSSH: "orca-lume; whoami"},
	} {
		if err := d.setComputerUse(invalid); err == nil {
			t.Fatalf("accepted invalid settings %+v", invalid)
		}
	}
	saved, _, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if saved.computerUseSettings != savedBefore || d.computerUse != savedBefore {
		t.Fatalf("invalid setting changed config: saved %+v, active %+v", saved.computerUseSettings, d.computerUse)
	}
}

func TestMacOSCoreLaunchFindsUserToolsWithFinderPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS Core launch")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	const finderPath = "/usr/bin:/bin:/usr/sbin:/sbin"
	t.Setenv("PATH", finderPath)
	cfg, paths, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.core, "server"), nil, 0700); err != nil {
		t.Fatal(err)
	}
	slot := update.Installed{Dir: paths.core, Manifest: update.Manifest{
		Executable: "server", WorkingDirectory: ".", HealthPath: "/health",
	}}
	spec, err := cfg.launchSpec(paths, &slot)
	if err != nil {
		t.Fatal(err)
	}
	want := finderPath + ":/opt/homebrew/bin:/usr/local/bin:" + filepath.Join(root, ".local/bin")
	if got := spec.Env["PATH"]; got != want {
		t.Fatalf("Core PATH = %q, want %q", got, want)
	}

	cfg.Environment["PATH"] = "/custom/bin"
	spec, err = cfg.launchSpec(paths, &slot)
	if err != nil {
		t.Fatal(err)
	}
	if got := spec.Env["PATH"]; got != "/custom/bin" {
		t.Fatalf("explicit Core PATH = %q", got)
	}
}

func TestCoreLaunchExposesComputerUseConfigOnMacOS(t *testing.T) {
	cfg, paths, err := loadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.core, "server"), nil, 0700); err != nil {
		t.Fatal(err)
	}
	slot := update.Installed{Dir: paths.core, Manifest: update.Manifest{
		Executable: "server", WorkingDirectory: ".", HealthPath: "/health",
	}}
	spec, err := cfg.launchSpec(paths, &slot)
	if err != nil {
		t.Fatal(err)
	}
	path, set := spec.Env["ORCA_WAILS_COMPUTER_USE_CONFIG_PATH"]
	if runtime.GOOS == "darwin" {
		if !set || !filepath.IsAbs(path) || path != paths.settings {
			t.Fatalf("expected absolute config path %q, got %q", paths.settings, path)
		}
		if got := spec.Env["ORCA_COMPUTER_MACOS_HELPER_APP_PATH"]; got != spec.Dir {
			t.Fatalf("old Core could still select the local Computer Use helper: %q", got)
		}
		slot.Manifest.Environment = map[string]string{
			"ORCA_WAILS_CUA_DRIVER_PATH":          "{core_dir}/cua-driver",
			"ORCA_COMPUTER_MACOS_HELPER_APP_PATH": "{core_dir}/orca-helper",
		}
		modern, err := cfg.launchSpec(paths, &slot)
		if err != nil {
			t.Fatal(err)
		}
		if got := modern.Env["ORCA_COMPUTER_MACOS_HELPER_APP_PATH"]; got != filepath.Join(modern.Dir, "orca-helper") {
			t.Fatalf("updated Core must retain the Orca helper: %q", got)
		}
	} else if set {
		t.Fatalf("unexpected config path on %s: %q", runtime.GOOS, path)
	}
}

func TestLoadConfigWithRetiredBrowserExtensions(t *testing.T) {
	root := t.TempDir()
	_, p, err := loadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.settings, []byte(`{"macos_browser_extensions":[{"directory":"/old-extension","sites":["https://example.org/*"]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadConfig(root); err != nil {
		t.Fatal(err)
	}
}

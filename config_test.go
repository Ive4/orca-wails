package main

import (
	"os"
	"path/filepath"
	"testing"
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

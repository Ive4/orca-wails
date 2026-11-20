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

package official

import (
	"path/filepath"
	"testing"
)

func TestSwapKeepsBackupAndRestoresRuntime(t *testing.T) {
	source, target, _ := importFixture(t)
	backup, err := Prepare(source, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := Swap(target, backup); err != nil {
		t.Fatal(err)
	}
	wantFile(t, filepath.Join(backup, "existing"), "keep this")
	wantAbsent(t, filepath.Join(target, "existing"))
	wantAbsent(t, filepath.Join(target, "agent-session-domain.db"))

	if err := Swap(target, filepath.Join(filepath.Dir(target), "missing")); err == nil {
		t.Fatal("Swap succeeded without a backup directory")
	}
	wantFile(t, filepath.Join(backup, "existing"), "keep this")

	if err := Swap(target, backup); err != nil {
		t.Fatal(err)
	}
	wantFile(t, filepath.Join(target, "existing"), "keep this")
}

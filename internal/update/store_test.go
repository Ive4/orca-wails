package update

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const macTarget = "aarch64-apple-darwin"
const windowsTarget = "x86_64-pc-windows-msvc"

type zipEntry struct {
	name string
	body string
	mode fs.FileMode
}

func fixture(t *testing.T, version, revision, target string, extra ...zipEntry) (string, string) {
	t.Helper()
	manifest := fmt.Sprintf(`{"schema_version":1,"version":%q,"target":%q,"executable":"runtimes/orcad","environment":{"ORCA_VERSION":"build-slot"}`, version, target)
	if revision != "" {
		manifest += fmt.Sprintf(`,"package_revision":%q`, revision)
	}
	entries := []zipEntry{
		{"core-manifest.json", manifest + "}", 0o600},
		{"runtimes/orcad", "core executable", 0o755},
	}
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, entry := range append(entries, extra...) {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "core.zip")
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, fmt.Sprintf("%x", sha256.Sum256(data.Bytes()))
}

func TestCoreTransactionAndRollback(t *testing.T) {
	s := Store{Root: filepath.Join(t.TempDir(), "core"), Target: macTarget}
	oldPath, oldSHA := fixture(t, "v1.4.265", "0.0.1", macTarget)
	newPath, newSHA := fixture(t, "v1.4.266", "0.0.4", macTarget)
	if got, err := s.Current(); err != nil || got != nil {
		t.Fatalf("uninitialized Current = %v, %v", got, err)
	}
	old, err := s.PrepareArchive(oldPath, strings.ToUpper(oldSHA))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(s.Root, "versions", "v1.4.265-aarch64-apple-darwin-"+oldSHA[:16]); old.Dir != want || old.SHA256 != oldSHA {
		t.Fatalf("old = %+v, want slot %s", old, want)
	}
	if err := s.Commit(old); err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(s.Root, "active.json")
	before, err := os.ReadFile(pointer)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := s.PrepareArchive(newPath, newSHA)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(pointer)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("preparation changed the active pointer: %s, %v", after, err)
	}
	if err := s.Commit(candidate); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(candidate); err != nil {
		t.Fatal(err)
	}
	previous, err := s.Previous()
	if err != nil || previous == nil || previous.Dir != old.Dir {
		t.Fatalf("Previous = %v, %v", previous, err)
	}
	if err := s.Commit(previous); err != nil {
		t.Fatal(err)
	}
	current, err := s.Current()
	if err != nil || current == nil || current.Dir != old.Dir {
		t.Fatalf("rollback Current = %v, %v", current, err)
	}
	previous, err = s.Previous()
	if err != nil || previous == nil || previous.Dir != candidate.Dir {
		t.Fatalf("rollback Previous = %v, %v", previous, err)
	}
}

func TestReadRustPointerAndReceipt(t *testing.T) {
	s := Store{Root: t.TempDir(), Target: macTarget}
	digest := strings.Repeat("a", 64)
	name := "v1.4.265-aarch64-apple-darwin-aaaaaaaaaaaaaaaa"
	dir := filepath.Join(s.Root, "versions", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, contents := range map[string]string{
		filepath.Join(s.Root, "active.json"):           fmt.Sprintf(`{"schema_version":1,"current":{"directory":%q,"version":"v1.4.265","archive_sha256":%q},"previous":null}`, name, digest),
		filepath.Join(dir, ".orca-tauri-install.json"): fmt.Sprintf(`{"schema_version":1,"version":"v1.4.265","target":"aarch64-apple-darwin","archive_sha256":%q}`, digest),
		filepath.Join(dir, "core-manifest.json"):       `{"schema_version":1,"version":"v1.4.265","target":"aarch64-apple-darwin","executable":"orcad","slot_version":"ignored"}`,
		filepath.Join(dir, "orcad"):                    "core executable",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	core, err := s.Current()
	if err != nil || core == nil {
		t.Fatalf("Current = %v, %v", core, err)
	}
	if m := core.Manifest; m.DisplayVersion() != "0.0.0-1.4.265" || m.WorkingDirectory != "." || m.HealthPath != "/web-index.html" || m.UIPath != "/web-index.html" {
		t.Fatalf("legacy manifest = %+v", m)
	}
	if err := os.WriteFile(filepath.Join(dir, ".orca-tauri-install.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current(); err == nil {
		t.Fatal("accepted corrupted receipt")
	}
}

func TestRejectUnsafeArchives(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries []zipEntry
	}{
		{"traversal", []zipEntry{{"../escape", "bad", 0o600}}},
		{"windows device", []zipEntry{{"path/com².txt", "bad", 0o600}}},
		{"windows stream", []zipEntry{{"path/bun:stream", "bad", 0o600}}},
		{"symlink", []zipEntry{{"link", "../escape", fs.ModeSymlink | 0o777}}},
		{"implicit case collision", []zipEntry{{"Modules/a", "a", 0o600}, {"modules/b", "b", 0o600}}},
		{"reserved receipt", []zipEntry{{".ORCA-TAURI-INSTALL.JSON", "bad", 0o600}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, digest := fixture(t, "v1.4.266", "", macTarget, test.entries...)
			s := Store{Root: t.TempDir(), Target: macTarget}
			if _, err := s.PrepareArchive(path, digest); err == nil {
				t.Fatal("unsafe archive prepared")
			}
			if _, err := InspectArchive(path); err == nil {
				t.Fatal("unsafe archive passed inspection")
			}
		})
	}
}

func TestPrepareChecksDigestAndTarget(t *testing.T) {
	path, digest := fixture(t, "v1.4.266", "0.0.4", macTarget)
	s := Store{Root: t.TempDir(), Target: macTarget}
	if _, err := s.PrepareArchive(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted incorrect digest")
	}
	s.Target = windowsTarget
	if _, err := s.PrepareArchive(path, digest); err == nil {
		t.Fatal("accepted archive for a different target")
	}
	manifest, err := InspectArchive(path)
	if err != nil || manifest.RuntimeVersion() != "build-slot" || manifest.DisplayVersion() != "0.0.4-1.4.266" {
		t.Fatalf("InspectArchive = %+v, %v", manifest, err)
	}
}

func TestCommitRejectsChangedCandidate(t *testing.T) {
	path, digest := fixture(t, "v1.4.266", "", macTarget)
	s := Store{Root: t.TempDir(), Target: macTarget}
	core, err := s.PrepareArchive(path, digest)
	if err != nil {
		t.Fatal(err)
	}
	core.Manifest.Environment["ORCA_VERSION"] = "different runtime"
	if err := s.Commit(core); err == nil {
		t.Fatal("committed candidate whose manifest changed")
	}
	if got, err := s.Current(); err != nil || got != nil {
		t.Fatalf("rejected commit changed Current = %v, %v", got, err)
	}
}

func TestStoreRejectsSymlinkRootWithTrailingSeparator(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	path, digest := fixture(t, "v1.4.266", "", macTarget)
	s := Store{Root: alias + string(os.PathSeparator), Target: macTarget}
	if _, err := s.PrepareArchive(path, digest); err == nil {
		t.Fatal("prepared archive through a symlinked store root")
	}
	if entries, err := os.ReadDir(real); err != nil || len(entries) != 0 {
		t.Fatalf("linked directory changed: %v, %v", entries, err)
	}
}

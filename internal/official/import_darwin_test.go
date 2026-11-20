package official

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const profileFixture = `
PRAGMA user_version=3;
PRAGMA journal_mode=WAL;
CREATE TABLE profile_state_documents(domain TEXT, payload TEXT, content_hash TEXT);
CREATE TABLE profile_state_document_parts(domain TEXT);
CREATE TABLE profile_state_document_parts_meta(domain TEXT);
CREATE TABLE profile_state_automation_runs(run TEXT);
CREATE TABLE profile_state_automation_runs_meta(domain TEXT PRIMARY KEY, presence TEXT, domain_version INTEGER, revision INTEGER, updated_at INTEGER, content_hash TEXT);
INSERT INTO profile_state_automation_runs_meta VALUES ('automationRuns','array',1,1,1,'abc');
INSERT INTO profile_state_automation_runs VALUES ('run');
INSERT INTO profile_state_documents VALUES
    ('projects','[{}]','a'),('settings','{}','b'),
    ('repos','[{"workspaceCleanupPolicy":{},"path":"/tmp/example''s & repo"}]','c');
INSERT INTO profile_state_document_parts VALUES ('worktreeMeta'),('settings');
INSERT INTO profile_state_document_parts_meta VALUES ('worktreeMeta'),('settings');
`

const historyFixture = `
PRAGMA user_version=2;
CREATE TABLE agent_session_records(session_id TEXT, record_json TEXT);
CREATE TABLE agent_session_operations(op TEXT);
CREATE TABLE agent_session_retired_claim_keys(key TEXT);
CREATE TABLE journal_queue_cards(card TEXT);
CREATE TABLE journal_edit_leases(lease TEXT);
CREATE TABLE journal_submission_attempts(attempt TEXT);
CREATE TABLE journal_items(payload_json TEXT, wire_json TEXT, wire_bytes INTEGER, detail_json TEXT);
CREATE TABLE journal_events(changes_json TEXT);
INSERT INTO agent_session_operations VALUES ('operation');
INSERT INTO agent_session_retired_claim_keys VALUES ('key');
INSERT INTO journal_queue_cards VALUES ('pending');
INSERT INTO journal_edit_leases VALUES ('lease');
INSERT INTO journal_submission_attempts VALUES ('attempt');
`

const indexFixture = `{"schemaVersion":1,"activeProfileId":"profile-1","profiles":[
{"id":"profile-1","name":"Work","avatar":{"kind":"initials","initials":"W","color":"neutral"},"kind":"cloud-linked","cloud":{},"createdAt":1,"updatedAt":2,"lastOpenedAt":3},
{"id":"other","name":"Other","avatar":null,"kind":"local","createdAt":1,"updatedAt":1,"lastOpenedAt":1}]}`

func TestPrepareImportsSavedData(t *testing.T) {
	source, target, profile := importFixture(t)
	writeFile(t, filepath.Join(source, "orca-devices.json"), "private")
	sqlite(t, source, "agent-session-domain.db", historyFixture)

	upload := "00000000-0000-0000-0000-000000000001"
	attachments := filepath.Join(source, "agent-session-attachments", upload)
	image := filepath.Join(attachments, "image.png")
	writeFile(t, image, "saved attachment")
	writeFile(t, filepath.Join(attachments, ".upload.part"), "unfinished")
	writeFile(t, filepath.Join(attachments, "nested", "ignore"), "nested")
	if err := os.Symlink(image, filepath.Join(attachments, "linked.png")); err != nil {
		t.Fatal(err)
	}
	// Seed real JSON with paths made of quotes, apostrophes, and multibyte characters.
	wire, err := json.Marshal(map[string]string{"attachment": image})
	if err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf(`{"attachment":%s,"rewind":{},"conversationCommand":{},"launchArgs":{},"usageLimitContinueCancel":{},"lease":{"ownerProcess":{"pid":10},"claimStatus":"held","unreconciled":true,"identity":"keep"}}`, jsonString(t, image))
	sqlite(t, source, "agent-session-domain.db", fmt.Sprintf(`
INSERT INTO agent_session_records VALUES ('s', %s);
INSERT INTO journal_items VALUES (%s,%s,%d,%s);
INSERT INTO journal_events VALUES (%s);`,
		literal(record), literal(string(wire)), literal(string(wire)), len(wire), literal(string(wire)), literal(string(wire))))

	staging, err := Prepare(source, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(staging) })
	if filepath.Dir(staging) != filepath.Dir(target) {
		t.Fatalf("staging %q is not next to target %q", staging, target)
	}
	wantFile(t, filepath.Join(target, "existing"), "keep this")
	wantFile(t, filepath.Join(source, "orca-devices.json"), "private")
	wantAbsent(t, filepath.Join(staging, "orca-devices.json"))
	installed := filepath.Join(target, "agent-session-attachments", upload, "image.png")
	attachmentStage := filepath.Join(staging, "agent-session-attachments", upload)
	wantFile(t, filepath.Join(attachmentStage, "image.png"), "saved attachment")
	for _, name := range []string{".upload.part", "nested", "linked.png"} {
		wantAbsent(t, filepath.Join(attachmentStage, name))
	}
	wantFile(t, filepath.Join(staging, "orca-profile-index.json"),
		`{"schemaVersion":1,"activeProfileId":"profile-1","profiles":[{"id":"profile-1","name":"Work","avatar":{"kind":"initials","initials":"W","color":"neutral"},"kind":"local","createdAt":1,"updatedAt":2,"lastOpenedAt":3}]}`)

	stagedProfile := filepath.Join(staging, "profiles", "profile-1")
	repos := `[{"path":"/tmp/example's & repo"}]`
	wantSQL(t, stagedProfile, "profile-state.db", `
SELECT domain FROM profile_state_documents ORDER BY domain;
SELECT payload,content_hash FROM profile_state_documents WHERE domain='repos';
SELECT domain FROM profile_state_document_parts;
SELECT domain FROM profile_state_document_parts_meta;
SELECT count(*) FROM profile_state_automation_runs;
SELECT presence,domain_version,revision,updated_at,content_hash FROM profile_state_automation_runs_meta;`,
		fmt.Sprintf("projects\nrepos\n%s|%x\nworktreeMeta\nworktreeMeta\n0\ndocument|1|0|0|", repos, sha256.Sum256([]byte(repos))))
	wantSQL(t, staging, "agent-session-domain.db", `
SELECT json_extract(record_json,'$.attachment','$.lease.claimStatus','$.lease.ownerProcess','$.lease.unreconciled','$.lease.identity') FROM agent_session_records;
SELECT count(*) FROM agent_session_records WHERE json_type(record_json,'$.rewind') IS NULL AND json_type(record_json,'$.conversationCommand') IS NULL AND json_type(record_json,'$.launchArgs') IS NULL AND json_type(record_json,'$.usageLimitContinueCancel') IS NULL;
SELECT count(*) FROM agent_session_operations;
SELECT count(*) FROM agent_session_retired_claim_keys;
SELECT count(*) FROM journal_queue_cards;
SELECT count(*) FROM journal_edit_leases;
SELECT count(*) FROM journal_submission_attempts;
SELECT wire_bytes = length(CAST(wire_json AS BLOB)), json_extract(payload_json,'$.attachment'), json_extract(wire_json,'$.attachment'), json_extract(detail_json,'$.attachment') FROM journal_items;
SELECT json_extract(changes_json,'$.attachment') FROM journal_events;`,
		fmt.Sprintf("[%s,\"released\",null,false,\"keep\"]\n1\n0\n0\n0\n0\n0\n1|%s|%s|%s\n%s", jsonString(t, installed), installed, installed, installed, installed))
	wantSQL(t, profile, "profile-state.db", "SELECT count(*) FROM profile_state_documents WHERE domain='settings';", "1")
	wantSQL(t, source, "agent-session-domain.db", "SELECT card FROM journal_queue_cards;", "pending")
}

func TestPrepareReadsCommittedWAL(t *testing.T) {
	source, target, profile := importFixture(t)
	database := filepath.Join(profile, "profile-state.db")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-batch", "-bail", "-init", "/dev/null", database)
	in, err := writer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := writer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	writer.Stderr = os.Stderr
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		in.Close()
		writer.Wait()
	}()
	if _, err := io.WriteString(in, `
PRAGMA wal_autocheckpoint=0;
INSERT INTO profile_state_documents VALUES ('worktreeMeta','[1]','');
BEGIN;
INSERT INTO profile_state_documents VALUES ('sparsePresetsByRepo','[2]','');
.print ready
`); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	for scanner.Scan() && scanner.Text() != "ready" {
	}
	if scanner.Text() != "ready" {
		t.Fatalf("SQLite writer failed to initialize: %v", scanner.Err())
	}
	if info, err := os.Stat(database + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("expected a live WAL: %v, %v", info, err)
	}

	staging, err := Prepare(source, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(staging) })
	wantSQL(t, filepath.Join(staging, "profiles", "profile-1"), "profile-state.db",
		"SELECT domain FROM profile_state_documents ORDER BY domain;", "projects\nrepos\nworktreeMeta")
}

func TestPrepareRejectsUnsupportedDataAndCleansStage(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(t *testing.T, source, profile string)
	}{
		{"index version", func(t *testing.T, source, _ string) {
			writeFile(t, filepath.Join(source, "orca-profile-index.json"), strings.Replace(indexFixture, `"schemaVersion":1`, `"schemaVersion":2`, 1))
		}},
		{"invalid profile ID", func(t *testing.T, source, _ string) {
			writeFile(t, filepath.Join(source, "orca-profile-index.json"), strings.ReplaceAll(indexFixture, "profile-1", "../profile-1"))
		}},
		{"profile DB version", func(t *testing.T, _, profile string) {
			sqlite(t, profile, "profile-state.db", "PRAGMA user_version=4;")
		}},
		{"corrupt profile DB", func(t *testing.T, _, profile string) {
			writeFile(t, filepath.Join(profile, "profile-state.db"), "corrupt database")
		}},
		{"session DB version", func(t *testing.T, source, _ string) {
			sqlite(t, source, "agent-session-domain.db", "PRAGMA user_version=3;")
		}},
		{"legacy history", func(t *testing.T, source, _ string) {
			writeFile(t, filepath.Join(source, "agent-session-journal.db"), "legacy")
		}},
		{"symlink DB", func(t *testing.T, _, profile string) {
			db := filepath.Join(profile, "profile-state.db")
			real := filepath.Join(profile, "real.db")
			if err := os.Rename(db, real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, db); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			source, target, profile := importFixture(t)
			tt.change(t, source, profile)
			if staging, err := Prepare(source, target); err == nil || staging != "" {
				t.Fatalf("Prepare = %q, %v", staging, err)
			}
			entries, err := os.ReadDir(filepath.Dir(target))
			if err != nil || len(entries) != 2 {
				t.Fatalf("expected only source and target after error, got %v, %v", entries, err)
			}
			wantFile(t, filepath.Join(target, "existing"), "keep this")
		})
	}
}

func TestPrepareRejectsOverlappingAndSymlinkTargets(t *testing.T) {
	source, target, _ := importFixture(t)
	nested := filepath.Join(source, "profiles")
	alias := filepath.Join(filepath.Dir(target), "alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	for _, paths := range [][2]string{{source, source}, {source, nested},
		{source, filepath.Dir(source)}, {source, alias}} {
		if stage, err := Prepare(paths[0], paths[1]); err == nil || stage != "" {
			t.Fatalf("Prepare(%q, %q) = %q, %v", paths[0], paths[1], stage, err)
		}
	}
}

func importFixture(t *testing.T) (source, target, profile string) {
	t.Helper()
	base := t.TempDir()
	source = filepath.Join(base, `official's "数据"`)
	target = filepath.Join(base, `orca-data's 目录`)
	profile = filepath.Join(source, "profiles", "profile-1")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(target, "existing"), "keep this")
	writeFile(t, filepath.Join(source, "orca-profile-index.json"), indexFixture)
	sqlite(t, profile, "profile-state.db", profileFixture)
	return source, target, profile
}

func sqlite(t *testing.T, directory, filename, query string) string {
	t.Helper()
	result, err := runSQLite(filepath.Join(directory, filename), directory, false, query)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func wantSQL(t *testing.T, directory, filename, query, want string) {
	t.Helper()
	if got := sqlite(t, directory, filename, query); got != want {
		t.Fatalf("SQLite output\n got %s\nwant %s", got, want)
	}
}

func jsonString(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func literal(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func wantFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("read %s = %q, %v", path, got, err)
	}
}

func wantAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be absent, got %v", path, err)
	}
}

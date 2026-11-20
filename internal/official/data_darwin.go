package official

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// These are the project and worktree documents in the supported profile database.
const projectDomains = `('schemaVersion','repos','projects','projectHostSetups',
    'projectGroups','folderWorkspaces','sparsePresetsByRepo','retiredWorktreeNamesByRepo',
    'retiredWorktreeNamesByNamespace','worktreeMeta','worktreeMetaByIdentity',
    'worktreeIdentityAliases','worktreeLineageById','workspaceLineageByChildKey')`

func importProjects(directory string) error {
	database := filepath.Join(directory, profileDB)
	_, err := runSQLite(database, directory, false, fmt.Sprintf(`
BEGIN;
DELETE FROM profile_state_documents WHERE domain NOT IN %[1]s;
DELETE FROM profile_state_document_parts WHERE domain NOT IN %[1]s;
DELETE FROM profile_state_document_parts_meta WHERE domain NOT IN %[1]s;
DELETE FROM profile_state_automation_runs;
INSERT OR REPLACE INTO profile_state_automation_runs_meta
    (domain, presence, domain_version, revision, updated_at, content_hash)
    VALUES ('automationRuns', 'document', 1, 0, 0, '');
COMMIT;
VACUUM;`, projectDomains))
	if err != nil {
		return err
	}

	payload, err := runSQLite(database, directory, true,
		"SELECT payload FROM profile_state_documents WHERE domain='repos';")
	if err != nil || payload == "" {
		return err
	}
	var repos []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &repos); err != nil {
		return err
	}
	if repos == nil {
		return errors.New("官方 Orca 的仓库数据无效")
	}
	changed := false
	for _, repo := range repos {
		if repo == nil {
			return errors.New("官方 Orca 的仓库数据无效")
		}
		if _, found := repo["workspaceCleanupPolicy"]; found {
			delete(repo, "workspaceCleanupPolicy")
			changed = true
		}
	}
	if !changed {
		return nil
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(repos); err != nil {
		return err
	}
	encoded := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
	_, err = runSQLite(database, directory, false, fmt.Sprintf(`
PRAGMA secure_delete=ON;
UPDATE profile_state_documents SET payload=%s,content_hash='%x' WHERE domain='repos';`,
		sqlLiteral(string(encoded)), sha256.Sum256(encoded)))
	return err
}

func importHistory(source, staged, target string) error {
	attachmentSource := filepath.Join(source, attachments)
	hasAttachments, err := exists(attachmentSource)
	if err != nil {
		return err
	}
	if hasAttachments {
		if err := copyAttachments(attachmentSource, filepath.Join(staged, attachments)); err != nil {
			return err
		}
	}
	if !utf8.ValidString(source) || !utf8.ValidString(target) {
		return errors.New("附件路径编码无效")
	}

	// Saved messages and identity fences are portable. Live claims and
	// pending commands belong to the original runtime.
	_, err = runSQLite(filepath.Join(staged, sessionDB), staged, false, fmt.Sprintf(`
BEGIN;
UPDATE agent_session_records SET record_json = json_set(
    json_remove(replace(record_json, %[1]s, %[2]s),
        '$.rewind', '$.conversationCommand', '$.usageLimitContinueCancel', '$.launchArgs'),
    '$.lease.claimStatus', 'released', '$.lease.ownerProcess', NULL,
    '$.lease.reservedSpawnToken', NULL, '$.lease.handoffStage', NULL,
    '$.lease.handoffOperationId', NULL, '$.lease.deathEvidence', NULL,
    '$.lease.unreconciled', json('false'));
DELETE FROM agent_session_operations;
DELETE FROM agent_session_retired_claim_keys;
DELETE FROM journal_queue_cards;
DELETE FROM journal_edit_leases;
DELETE FROM journal_submission_attempts;
UPDATE journal_items SET
    payload_json = replace(payload_json, %[3]s, %[4]s),
    wire_json = replace(wire_json, %[3]s, %[4]s),
    detail_json = replace(detail_json, %[3]s, %[4]s),
    wire_bytes = wire_bytes + coalesce(
        length(CAST(replace(wire_json, %[3]s, %[4]s) AS BLOB))
        - length(CAST(wire_json AS BLOB)), 0)
    WHERE instr(payload_json, %[3]s) OR instr(wire_json, %[3]s) OR instr(detail_json, %[3]s);
UPDATE journal_events SET changes_json = replace(changes_json, %[3]s, %[4]s)
    WHERE instr(changes_json, %[3]s);
COMMIT;
VACUUM;`, jsonPathPrefix(source), jsonPathPrefix(target),
		jsonPathPrefix(attachmentSource), jsonPathPrefix(filepath.Join(target, attachments))))
	return err
}

func jsonPathPrefix(path string) string {
	// The trailing slash lets us strip the closing JSON quote without
	// stripping an escaped quote that belongs to the path.
	return "rtrim(substr(json_quote(" + sqlLiteral(path+"/") + "), 2), '\"')"
}

func sqlLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func copyAttachments(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil || !info.IsDir() {
		return err
	}
	uploads, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, upload := range uploads {
		if !upload.IsDir() {
			continue
		}
		sourceUpload := filepath.Join(source, upload.Name())
		targetUpload := filepath.Join(target, upload.Name())
		files, err := os.ReadDir(sourceUpload)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !file.Type().IsRegular() || file.Name() == ".upload.part" {
				continue
			}
			if err := os.MkdirAll(targetUpload, 0700); err != nil {
				return err
			}
			if err := copyFile(filepath.Join(sourceUpload, file.Name()), filepath.Join(targetUpload, file.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

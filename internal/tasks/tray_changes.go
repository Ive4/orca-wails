package tasks

import (
	"context"
	"errors"
	"path"
	"strings"
)

const maxTrayFiles = 40
const maxTrayDiffBytes = 32 * 1024
const maxTrayDiffLines = 400

var ErrTrayChanges = errors.New("暂时无法读取这个工作区的 Git 改动")
var ErrTrayDiff = errors.New("该文件的改动已变化，请刷新列表")

type TrayChange struct {
	Path     string `json:"path"`
	OldPath  string `json:"oldPath,omitempty"`
	Area     string `json:"area"`
	Status   string `json:"status"`
	Conflict string `json:"conflictStatus,omitempty"`
}

type TrayChanges struct {
	Entries []TrayChange `json:"entries"`
	Limited bool         `json:"limited"`
}

type TrayDiff struct {
	Before string `json:"originalContent"`
	After  string `json:"modifiedContent"`
	Kind   string `json:"kind"`
	Large  bool   `json:"tooLarge,omitempty"`
}

func (row Row) CanReadChanges() bool {
	id := row.Target.WorktreeID
	return row.Local && id != "" && !strings.HasPrefix(id, "folder:")
}

func ReadTrayChanges(ctx context.Context, data string, pid uint32, row Row) (TrayChanges, error) {
	if !row.CanReadChanges() {
		return TrayChanges{}, ErrTrayChanges
	}
	return withRuntime(ctx, data, pid, func(call rpcCall) (TrayChanges, error) {
		return readTrayChanges(call, row.Target.WorktreeID)
	})
}

func readTrayChanges(call rpcCall, workspace string) (TrayChanges, error) {
	var status struct {
		Entries   []TrayChange `json:"entries"`
		ReadError string       `json:"readError"`
		Limited   bool         `json:"didHitLimit"`
	}
	if err := call("git.status", map[string]any{
		"worktree": "id:" + workspace, "includeLineStats": false, "includeIgnored": false,
	}, &status); err != nil || status.ReadError != "" || status.Entries == nil {
		return TrayChanges{}, ErrTrayChanges
	}
	result := TrayChanges{Entries: []TrayChange{}, Limited: status.Limited}
	for _, entry := range status.Entries {
		if !validTrayChangePath(entry.Path) || (entry.OldPath != "" && !validTrayChangePath(entry.OldPath)) {
			continue
		}
		if entry.Area != "staged" && entry.Area != "unstaged" && entry.Area != "untracked" {
			continue
		}
		if len(result.Entries) == maxTrayFiles {
			result.Limited = true
			break
		}
		result.Entries = append(result.Entries, entry)
	}
	return result, nil
}

func ReadTrayDiff(ctx context.Context, data string, pid uint32, row Row, file, area string) (TrayDiff, error) {
	if !row.CanReadChanges() {
		return TrayDiff{}, ErrTrayChanges
	}
	return withRuntime(ctx, data, pid, func(call rpcCall) (TrayDiff, error) {
		return readTrayDiff(call, row.Target.WorktreeID, file, area)
	})
}

func readTrayDiff(call rpcCall, workspace, file, area string) (TrayDiff, error) {
	if !validTrayChangePath(file) {
		return TrayDiff{}, ErrTrayDiff
	}
	changes, err := readTrayChanges(call, workspace)
	if err != nil {
		return TrayDiff{}, err
	}
	for _, entry := range changes.Entries {
		if entry.Path != file || entry.Area != area || entry.Conflict == "unresolved" {
			continue
		}
		params := map[string]any{"worktree": "id:" + workspace, "filePath": file, "staged": area == "staged"}
		if entry.OldPath != "" {
			params["oldPath"] = entry.OldPath
		}
		var preview TrayDiff
		if err := call("git.diff", params, &preview); err != nil || (preview.Kind != "text" && preview.Kind != "binary") {
			return TrayDiff{}, errors.New("预览暂不可用，可在原工作区查看完整 diff")
		}
		preview.Large = preview.Large || len(preview.Before) > maxTrayDiffBytes || len(preview.After) > maxTrayDiffBytes ||
			strings.Count(preview.Before, "\n") > maxTrayDiffLines || strings.Count(preview.After, "\n") > maxTrayDiffLines
		if preview.Large || preview.Kind == "binary" {
			preview.Before, preview.After = "", ""
		}
		return preview, nil
	}
	return TrayDiff{}, ErrTrayDiff
}

func validTrayChangePath(file string) bool {
	return file != "" && file != "." && file != ".." && !path.IsAbs(file) &&
		!strings.HasPrefix(file, "../") && !strings.ContainsRune(file, 0) && path.Clean(file) == file
}

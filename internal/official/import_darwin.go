package official

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	profileDB   = "profile-state.db"
	sessionDB   = "agent-session-domain.db"
	attachments = "agent-session-attachments"
	indexFile   = "orca-profile-index.json"
)

type profileIndex struct {
	SchemaVersion   uint32    `json:"schemaVersion"`
	ActiveProfileID string    `json:"activeProfileId"`
	Profiles        []profile `json:"profiles"`
}

type profile struct {
	ID           *string         `json:"id"`
	Name         *string         `json:"name"`
	Avatar       json.RawMessage `json:"avatar"`
	Kind         *string         `json:"kind"`
	CreatedAt    *uint64         `json:"createdAt"`
	UpdatedAt    *uint64         `json:"updatedAt"`
	LastOpenedAt *uint64         `json:"lastOpenedAt"`
}

// Discover checks the installed official Orca version and returns its data directory.
func Discover() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法定位用户目录: %w", err)
	}
	for _, app := range []string{"/Applications/Orca.app", filepath.Join(home, "Applications", "Orca.app")} {
		info, err := os.Stat(app)
		if err != nil || !info.IsDir() {
			continue
		}
		output, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print CFBundleShortVersionString",
			filepath.Join(app, "Contents", "Info.plist")).Output()
		if err != nil || strings.TrimSpace(string(output)) != Version[1:] {
			return "", fmt.Errorf("当前导入支持官方 Orca %s", Version)
		}
		return filepath.Join(home, "Library", "Application Support", "orca"), nil
	}
	return "", errors.New("请先安装官方 Orca")
}

// Prepare builds an import in a temporary sibling of the existing target directory.
// On success the caller owns staging and must remove it on abort or retain it as
// the backup after Swap. On failure Prepare cleans up any staging it created.
func Prepare(source, target string) (staging string, err error) {
	if source == "" || target == "" {
		return "", errors.New("必须指定官方 Orca 与 Wails 的数据目录")
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return "", err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", err
	}
	targetInfo, err := checkDirectories(source, target)
	if err != nil {
		return "", err
	}
	index, err := readProfileIndex(source)
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp(filepath.Dir(target), "orca-data-before-import-")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			if cleanupErr := os.RemoveAll(dir); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("无法移除导入暂存目录: %w", cleanupErr))
			}
		}
	}()
	stageInfo, err := os.Stat(dir)
	if err != nil {
		return "", err
	}
	if stageInfo.Sys().(*syscall.Stat_t).Dev != targetInfo.Sys().(*syscall.Stat_t).Dev {
		return "", errors.New("导入暂存目录与 Wails 数据目录必须位于同一卷")
	}
	profilePath := filepath.Join("profiles", index.ActiveProfileID)
	destination := filepath.Join(dir, profilePath)
	if err := os.MkdirAll(destination, 0700); err != nil {
		return "", err
	}
	if err := snapshot(filepath.Join(source, profilePath), destination, profileDB, 3); err != nil {
		return "", err
	}
	if err := importProjects(destination); err != nil {
		return "", fmt.Errorf("无法准备项目和工作树数据: %w", err)
	}
	payload, err := json.Marshal(index)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, indexFile), payload, 0600); err != nil {
		return "", err
	}
	hasHistory, err := exists(filepath.Join(source, sessionDB))
	if err != nil {
		return "", err
	}
	if hasHistory {
		if err := snapshot(source, dir, sessionDB, 2); err != nil {
			return "", err
		}
		if err := importHistory(source, dir, target); err != nil {
			return "", fmt.Errorf("无法准备历史对话: %w", err)
		}
	} else {
		legacy, err := exists(filepath.Join(source, "agent-session-journal.db"))
		if err != nil {
			return "", err
		}
		if legacy {
			return "", errors.New("请先在官方 Orca 打开会话历史以完成升级，再重试导入")
		}
	}
	return dir, nil
}

func checkDirectories(source, target string) (os.FileInfo, error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return nil, fmt.Errorf("未找到官方 Orca 数据目录: %w", err)
	}
	targetInfo, err := os.Lstat(target)
	if err != nil {
		return nil, fmt.Errorf("无法定位 Wails 数据目录: %w", err)
	}
	independent := errors.New("官方 Orca 与 Wails 必须使用独立的数据目录")
	if !sourceInfo.IsDir() || !targetInfo.IsDir() {
		return nil, independent
	}
	for _, pair := range []struct {
		path  string
		other os.FileInfo
	}{{source, targetInfo}, {target, sourceInfo}} {
		resolved, err := filepath.EvalSymlinks(pair.path)
		if err != nil {
			return nil, err
		}
		for path := resolved; ; path = filepath.Dir(path) {
			info, err := os.Stat(path)
			if err != nil {
				return nil, err
			}
			// Compare identities so alternate spellings on case-insensitive volumes
			// cannot bypass the containment check.
			if os.SameFile(info, pair.other) {
				return nil, independent
			}
			if filepath.Dir(path) == path {
				break
			}
		}
	}
	return targetInfo, nil
}

func readProfileIndex(source string) (profileIndex, error) {
	var index profileIndex
	payload, err := os.ReadFile(filepath.Join(source, indexFile))
	if err != nil {
		return index, fmt.Errorf("未找到官方 Orca 的本地资料: %w", err)
	}
	if err := json.Unmarshal(payload, &index); err != nil {
		return index, fmt.Errorf("无法读取官方 Orca 的资料索引: %w", err)
	}
	if index.SchemaVersion != 1 {
		return index, errors.New("官方 Orca 的资料格式不受支持")
	}
	var active []profile
	kind := "local"
	for _, p := range index.Profiles {
		if p.ID == nil || p.Name == nil || p.Avatar == nil || p.Kind == nil ||
			p.CreatedAt == nil || p.UpdatedAt == nil || p.LastOpenedAt == nil {
			return index, errors.New("官方 Orca 的资料索引缺少必需字段")
		}
		if *p.ID == index.ActiveProfileID {
			p.Kind = &kind
			active = append(active, p)
		}
	}
	if len(active) != 1 {
		return index, errors.New("未找到官方 Orca 当前使用的资料")
	}
	if !validProfileID(index.ActiveProfileID) {
		return index, errors.New("官方 Orca 的资料标识无效")
	}
	index.Profiles = active
	return index, nil
}

func validProfileID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for i, c := range []byte(id) {
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			(i > 0 && (c == '-' || c == '_')) {
			continue
		}
		return false
	}
	return true
}

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

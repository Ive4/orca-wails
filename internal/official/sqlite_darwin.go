package official

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

func snapshot(source, target, name string, version uint32) error {
	database := filepath.Join(source, name)
	before, err := os.Lstat(database)
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("官方 Orca 数据库不可读取")
	}
	hadJournal, err := hasJournal(database)
	if err != nil {
		return err
	}

	// SQLite's backup API reads committed WAL frames while Orca is running.
	if _, backupErr := runSQLite(database, target, true, ".backup "+name); backupErr != nil {
		// A closed WAL database can fail to open read-only without sidecars.
		// Fall back to copying only if no journal or database change is seen.
		hasJournalNow, err := hasJournal(database)
		if err != nil {
			return err
		}
		if hadJournal || hasJournalNow {
			return fmt.Errorf("无法创建 %s 快照 · %w", name, backupErr)
		}
		if err := copyFile(database, filepath.Join(target, name)); err != nil {
			return err
		}
		after, err := os.Lstat(database)
		if err != nil {
			return err
		}
		hasJournalNow, err = hasJournal(database)
		if err != nil {
			return err
		}
		if hasJournalNow || !os.SameFile(before, after) || before.Size() != after.Size() ||
			before.Sys().(*syscall.Stat_t).Ctimespec != after.Sys().(*syscall.Stat_t).Ctimespec {
			return errors.New("官方 Orca 数据刚刚发生变化，请重试")
		}
	}
	output, err := runSQLite(filepath.Join(target, name), target, false,
		"PRAGMA journal_mode=DELETE; PRAGMA quick_check; PRAGMA user_version;")
	if err != nil {
		return fmt.Errorf("无法验证 %s 快照: %w", name, err)
	}
	if output != fmt.Sprintf("delete\nok\n%d", version) {
		return fmt.Errorf("官方 Orca 的 %s 格式不受支持或完整性检查失败", name)
	}
	return nil
}

func hasJournal(database string) (bool, error) {
	if wal, err := exists(database + "-wal"); wal || err != nil {
		return wal, err
	}
	return exists(database + "-journal")
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	return errors.Join(copyErr, output.Close())
}

func runSQLite(database, directory string, readonly bool, statement string) (string, error) {
	args := []string{"-batch", "-bail", "-init", "/dev/null", "-cmd", ".timeout 3000"}
	if readonly {
		args = append(args, "-readonly")
	}
	command := exec.Command("/usr/bin/sqlite3", append(args, database)...)
	command.Dir = directory
	command.Stdin = strings.NewReader(statement + "\n")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("SQLite 处理失败 (%s): %w", strings.TrimSpace(stderr.String()), err)
	}
	if !utf8.Valid(output) {
		return "", errors.New("SQLite 输出编码无效")
	}
	return strings.TrimSpace(string(output)), nil
}

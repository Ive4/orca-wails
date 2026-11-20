package official

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// Swap atomically exchanges two existing directories on the same volume.
// The same call rolls the swap back. The caller must stop writers before either swap.
func Swap(active, backup string) error {
	for _, path := range []string{active, backup} {
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("无法切换 Wails 数据目录: %w", err)
		}
		if !info.IsDir() {
			return fmt.Errorf("无法切换 Wails 数据目录: %q 不是目录", path)
		}
	}
	// RENAME_SWAP leaves a complete directory at each name across interruption.
	if err := unix.RenameatxNp(unix.AT_FDCWD, active, unix.AT_FDCWD, backup, unix.RENAME_SWAP); err != nil {
		return fmt.Errorf("无法切换 Wails 数据目录: %w", err)
	}
	return nil
}

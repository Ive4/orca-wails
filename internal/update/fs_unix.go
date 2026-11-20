//go:build !windows

package update

import (
	"io/fs"
	"os"
)

func isLinkOrReparse(info fs.FileInfo) bool {
	return info.Mode()&fs.ModeSymlink != 0
}

func privateDirectory(path string) error { return os.Chmod(path, 0o700) }

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

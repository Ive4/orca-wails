package update

import (
	"io/fs"
	"syscall"
)

func isLinkOrReparse(info fs.FileInfo) bool {
	attrs, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&fs.ModeSymlink != 0 || (ok && attrs.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}

func privateDirectory(string) error { return nil }
func syncDirectory(string) error    { return nil }

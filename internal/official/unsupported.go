//go:build !darwin

package official

import (
	"errors"
	"fmt"
)

var errUnsupported = fmt.Errorf("当前平台不支持导入官方 Orca 数据: %w", errors.ErrUnsupported)

// Discover reports that importing official Orca data requires macOS.
func Discover() (string, error) { return "", errUnsupported }

// Prepare reports that importing official Orca data requires macOS.
func Prepare(source, target string) (string, error) { return "", errUnsupported }

// Swap reports that the importer requires the macOS atomic directory exchange API.
func Swap(active, backup string) error { return errUnsupported }

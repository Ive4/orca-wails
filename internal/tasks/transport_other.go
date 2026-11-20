//go:build !darwin && !linux && !windows

package tasks

import (
	"context"
	"errors"
	"io"
)

const metadataOpenFlags = 0

func connectOwned(context.Context, runtimeMetadata) (io.ReadWriteCloser, error) {
	return nil, errors.New("Orca transport peer PID verification is unavailable on this platform")
}

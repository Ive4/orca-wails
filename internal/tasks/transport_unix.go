//go:build darwin || linux

package tasks

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const metadataOpenFlags = unix.O_NOFOLLOW | unix.O_NONBLOCK

func connectOwned(ctx context.Context, metadata runtimeMetadata) (io.ReadWriteCloser, error) {
	endpoint, ok := metadata.endpoint("unix")
	if !ok {
		return nil, errors.New("missing Orca socket")
	}
	if !filepath.IsAbs(endpoint) {
		return nil, errors.New("expected a local Orca socket")
	}
	var dialer net.Dialer
	stream, err := dialer.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, err
	}

	// The metadata can be stale. Only the kernel can identify the listener.
	raw, err := stream.(*net.UnixConn).SyscallConn()
	if err == nil {
		var pid int
		var peerErr error
		err = raw.Control(func(fd uintptr) {
			pid, peerErr = peerPID(int(fd))
		})
		if err == nil {
			err = peerErr
		}
		if err == nil && (pid <= 0 || uint32(pid) != metadata.pid) {
			err = errors.New("Orca socket belongs to another process")
		}
	}
	if err != nil {
		_ = stream.Close()
		return nil, err
	}
	return stream, nil
}

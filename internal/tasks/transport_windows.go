package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const metadataOpenFlags = 0

func connectOwned(ctx context.Context, metadata runtimeMetadata) (io.ReadWriteCloser, error) {
	endpoint, ok := metadata.endpoint("named-pipe")
	if !ok {
		return nil, errors.New("missing Orca pipe")
	}
	suffix, ok := strings.CutPrefix(endpoint, fmt.Sprintf(`\\.\pipe\orca-%d-`, metadata.pid))
	if !ok {
		return nil, errors.New("expected a local Orca pipe")
	}
	if !validPipeSuffix(suffix) {
		return nil, errors.New("invalid Orca pipe name")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(endpoint)
	if err != nil {
		return nil, err
	}

	// Use identification-only SQOS and fail immediately if the pipe is busy.
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
	if err != nil {
		return nil, err
	}
	var peer uint32
	if err := windows.GetNamedPipeServerProcessId(handle, &peer); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if peer != metadata.pid {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("Orca pipe belongs to another process")
	}
	stream, err := winio.NewOpenFile(handle)
	if err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	return stream, nil
}

func validPipeSuffix(suffix string) bool {
	if len(suffix) < 1 || len(suffix) > 4 {
		return false
	}
	for _, ch := range suffix {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

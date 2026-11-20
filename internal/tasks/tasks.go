// Package tasks reads task status from the local Orca runtime.
package tasks

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	metadataLimit = 16 * 1024
	responseLimit = 8 * 1024 * 1024
	requestID     = "tray-tasks"
)

type Summary struct {
	Rows    []Row `json:"rows"`
	Partial bool  `json:"partial"`
}

type Row struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Workspace string  `json:"workspace"`
	Provider  *string `json:"provider"`
	Status    Status  `json:"status"`
	StartedAt *uint64 `json:"startedAt"`
	UpdatedAt *uint64 `json:"updatedAt"`
	Target    Target  `json:"-"`
}

type Target struct {
	WorktreeID string  `json:"worktreeId"`
	TabID      string  `json:"tabId"`
	LeafID     *string `json:"leafId"`
}

type Status string

const (
	Waiting    Status = "waiting"
	Blocked    Status = "blocked"
	Working    Status = "working"
	Monitoring Status = "monitoring"
	Unknown    Status = "unknown"
	Done       Status = "done"
)

// Read retrieves a task summary from the runtime with the given process ID.
func Read(ctx context.Context, data string, pid uint32) (summary Summary, err error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	defer func() {
		if ctxErr := ctx.Err(); ctxErr != nil {
			summary, err = Summary{}, ctxErr
		}
	}()
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	// Regular file I/O can block beyond the request deadline.
	type metadataResult struct {
		metadata runtimeMetadata
		err      error
	}
	ready := make(chan metadataResult, 1)
	go func() {
		metadata, err := readMetadata(data, pid)
		ready <- metadataResult{metadata, err}
	}()
	select {
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	case result := <-ready:
		if result.err != nil {
			return Summary{}, result.err
		}
		return readSummary(ctx, result.metadata)
	}
}

func readMetadata(data string, pid uint32) (runtimeMetadata, error) {
	path := filepath.Join(data, "orca-runtime.json")
	info, err := os.Lstat(path)
	if err != nil {
		return runtimeMetadata{}, err
	}
	if !info.Mode().IsRegular() {
		return runtimeMetadata{}, errors.New("expected local Orca runtime metadata file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|metadataOpenFlags, 0)
	if err != nil {
		return runtimeMetadata{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return runtimeMetadata{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return runtimeMetadata{}, errors.New("expected local Orca runtime metadata file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, metadataLimit+1))
	if err != nil {
		return runtimeMetadata{}, err
	}
	if len(raw) > metadataLimit {
		return runtimeMetadata{}, errors.New("Orca metadata too large")
	}
	var metadata runtimeMetadata
	if err := unmarshalWire(raw, &metadata); err != nil {
		return runtimeMetadata{}, fmt.Errorf("invalid Orca metadata: %w", err)
	}
	if metadata.pid != pid {
		return runtimeMetadata{}, errors.New("Orca metadata belongs to another process")
	}
	return metadata, nil
}

func readSummary(ctx context.Context, metadata runtimeMetadata) (Summary, error) {
	stream, err := connectOwned(ctx, metadata)
	if err != nil {
		return Summary{}, err
	}
	closeStream := sync.OnceFunc(func() { _ = stream.Close() })
	defer closeStream()
	stop := context.AfterFunc(ctx, closeStream)
	defer stop()
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}

	request := struct {
		ID        string `json:"id"`
		AuthToken string `json:"authToken"`
		Method    string `json:"method"`
		Params    struct {
			Limit                                    int  `json:"limit"`
			SupportsWorktreeVisibilitySourceDefaults bool `json:"supportsWorktreeVisibilitySourceDefaults"`
		} `json:"params"`
	}{ID: requestID, AuthToken: metadata.authToken, Method: "worktree.ps"}
	request.Params.Limit = 256
	request.Params.SupportsWorktreeVisibilitySourceDefaults = true
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		return Summary{}, err
	}

	line, err := bufio.NewReader(io.LimitReader(stream, responseLimit)).ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Summary{}, err
	}
	if !bytes.HasSuffix(line, []byte{'\n'}) {
		return Summary{}, errors.New("incomplete Orca response")
	}
	return parseResponse(line, metadata.runtimeID)
}

func (m runtimeMetadata) endpoint(kind string) (string, bool) {
	for _, transport := range m.transports {
		if transport.kind == kind {
			return transport.endpoint, true
		}
	}
	return "", false
}

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
	ID                       string  `json:"id"`
	Title                    string  `json:"title"`
	Workspace                string  `json:"workspace"`
	Provider                 *string `json:"provider"`
	Status                   Status  `json:"status"`
	StartedAt                *uint64 `json:"startedAt"`
	UpdatedAt                *uint64 `json:"updatedAt"`
	CompletedDurationSeconds *uint64 `json:"completedDurationSeconds,omitempty"`
	Target                   Target  `json:"-"`
	QuickReply               bool    `json:"quickReply,omitempty"`
	Outcome                  string  `json:"outcome,omitempty"`
	LaunchKey                string  `json:"launchKey,omitempty"`
	WorkspaceKey             string  `json:"workspaceKey,omitempty"`
	ConversationID           string  `json:"-"`
	TerminalAddress          string  `json:"-"`
	CurrentTool              *string `json:"currentTool,omitempty"`
	LastResult               *string `json:"-"`
	Local                    bool    `json:"-"`

	OutputMetrics *OutputMetrics `json:"outputMetrics,omitempty"`
}

type OutputMetrics struct {
	TurnOutputTokens       uint64 `json:"turnOutputTokens,omitempty"`
	VisibleTokensPerSecond uint64 `json:"visibleTokensPerSecond,omitempty"`
	VisibleUntil           uint64 `json:"visibleUntil,omitempty"`
	RequestRate            bool   `json:"requestRate,omitempty"`
	ToolActive             bool   `json:"toolActive,omitempty"`
}

type Target struct {
	WorktreeID    string  `json:"worktreeId"`
	TabID         string  `json:"tabId"`
	LeafID        *string `json:"leafId"`
	FocusComposer bool    `json:"focusComposer,omitempty"`
	ChatSessionID string  `json:"sessionId,omitempty"`
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

	metadata, err := runtimeFor(ctx, data, pid)
	if err != nil {
		return Summary{}, err
	}
	return readSummary(ctx, metadata)
}

func runtimeFor(ctx context.Context, data string, pid uint32) (runtimeMetadata, error) {
	if err := ctx.Err(); err != nil {
		return runtimeMetadata{}, err
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
		return runtimeMetadata{}, ctx.Err()
	case result := <-ready:
		return result.metadata, result.err
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
	line, err := runtimeRequest(ctx, metadata, requestID, "worktree.ps", struct {
		Limit                                    int  `json:"limit"`
		SupportsWorktreeVisibilitySourceDefaults bool `json:"supportsWorktreeVisibilitySourceDefaults"`
	}{256, true})
	if err != nil {
		return Summary{}, err
	}
	return parseResponse(line, metadata.runtimeID)
}

// CallLocal forwards a desktop request to its own Core and returns the RPC envelope.
func CallLocal(ctx context.Context, data string, pid uint32, method string, params json.RawMessage) (json.RawMessage, error) {
	stream, err := SubscribeLocal(ctx, data, pid, method, params)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return stream.Next()
}

type desktopQuestionKeyContext struct{}

func WithDesktopQuestionAccess(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, desktopQuestionKeyContext{}, key)
}

func runtimeRequest(ctx context.Context, metadata runtimeMetadata, id, method string, params any) ([]byte, error) {
	stream, closeStream, err := openRuntimeRequest(ctx, metadata, id, method, params, false)
	if err != nil {
		return nil, err
	}
	defer closeStream()
	reader := bufio.NewReader(io.LimitReader(stream, responseLimit))
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if !bytes.HasSuffix(line, []byte{'\n'}) {
			return nil, errors.New("incomplete Orca response")
		}
		// Core emits this frame while a launch waits for its agent to accept the prompt.
		if !bytes.Equal(bytes.TrimSpace(line), []byte(`{"_keepalive":true}`)) {
			return line, nil
		}
	}
}

func openRuntimeRequest(ctx context.Context, metadata runtimeMetadata, id, method string, params any, desktop bool) (io.Reader, func(), error) {
	stream, err := connectOwned(ctx, metadata)
	if err != nil {
		return nil, nil, err
	}
	closeStream := sync.OnceFunc(func() { _ = stream.Close() })
	stop := context.AfterFunc(ctx, closeStream)
	closeRequest := func() {
		stop()
		closeStream()
	}
	if err := ctx.Err(); err != nil {
		closeRequest()
		return nil, nil, err
	}

	request := struct {
		ID           string `json:"id"`
		AuthToken    string `json:"authToken"`
		Method       string `json:"method"`
		Params       any    `json:"params"`
		QuestionKey  string `json:"wailsQuestionKey,omitempty"`
		WailsDesktop bool   `json:"wailsDesktop,omitempty"`
	}{ID: id, AuthToken: metadata.authToken, Method: method, Params: params, WailsDesktop: desktop}
	switch {
	case desktop, method == "agentSession.history", method == "agentSession.item",
		method == "agentSession.respondToQuestion", method == "agent.read":
		request.QuestionKey, _ = ctx.Value(desktopQuestionKeyContext{}).(string)
	}
	if err := json.NewEncoder(stream).Encode(request); err != nil {
		closeRequest()
		return nil, nil, err
	}
	return stream, closeRequest, nil
}

type LocalSubscription struct {
	ctx       context.Context
	reader    *bufio.Scanner
	runtimeID string
	close     func()
}

const desktopRequestID = "wails-desktop"

func SubscribeLocal(ctx context.Context, data string, pid uint32, method string, params json.RawMessage) (*LocalSubscription, error) {
	metadata, err := runtimeFor(ctx, data, pid)
	if err != nil {
		return nil, err
	}
	stream, closeRequest, err := openRuntimeRequest(ctx, metadata, desktopRequestID, method, params, true)
	if err != nil {
		return nil, err
	}
	reader := bufio.NewScanner(stream)
	reader.Buffer(make([]byte, 32<<10), responseLimit)
	return &LocalSubscription{ctx, reader, metadata.runtimeID, closeRequest}, nil
}

func (s *LocalSubscription) Close() { s.close() }

func (s *LocalSubscription) Next() (json.RawMessage, error) {
	for s.ctx.Err() == nil && s.reader.Scan() {
		frame := s.reader.Bytes()
		if bytes.Equal(frame, []byte(`{"_keepalive":true}`)) {
			continue
		}
		if bytes.Equal(frame, []byte(`{"_wailsDone":true}`)) {
			return nil, io.EOF
		}
		var id string
		var ok bool
		var meta responseMeta
		if err := decodeObject(frame, required("id", &id), required("ok", &ok), required("_meta", &meta)); err != nil {
			return nil, err
		}
		if id != desktopRequestID || meta.runtimeID != s.runtimeID {
			return nil, errors.New("response belongs to another Orca request or runtime")
		}
		return bytes.Clone(frame), nil
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.reader.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (m runtimeMetadata) endpoint(kind string) (string, bool) {
	for _, transport := range m.transports {
		if transport.kind == kind {
			return transport.endpoint, true
		}
	}
	return "", false
}

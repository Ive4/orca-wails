package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"orca-wails/internal/tasks"
)

const desktopRPCPath = "/desktop/rpc"

type desktopStream struct {
	frames   chan json.RawMessage
	cancel   context.CancelFunc
	lastPoll atomic.Int64
}

func (w *orcaWindow) cancelDesktop() {
	if w.desktopCancel != nil {
		w.desktopCancel()
	}
}

func (d *desktop) serveDesktopAssets(w http.ResponseWriter, r *http.Request) {
	current := d.window.Load()
	if current == nil || current.desktopDir == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == desktopRPCPath {
		d.serveDesktopRPC(w, r, current)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/desktop/")
	if name == "index.html" {
		name = "wails-index.html"
	} else if !strings.HasPrefix(name, "assets/") &&
		!strings.HasPrefix(name, "cmaps/") && !strings.HasPrefix(name, "standard_fonts/") &&
		!strings.HasPrefix(name, "wasm/") {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(current.desktopDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'none'; object-src 'none'")
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (d *desktop) serveDesktopRPC(w http.ResponseWriter, r *http.Request, current *orcaWindow) {
	if current.desktopToken == "" || subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("X-Orca-Desktop")), []byte(current.desktopToken)) != 1 {
		http.Error(w, "desktop window required", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Action string          `json:"action"`
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	body := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	body.DisallowUnknownFields()
	if err := body.Decode(&request); err != nil || len(request.Method) > 120 || len(request.ID) > 80 ||
		body.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid desktop request", http.StatusBadRequest)
		return
	}
	if request.Action == "poll" || request.Action == "cancel" {
		d.pollDesktopStream(w, r, current, request.ID, request.Action == "cancel")
		return
	}
	if request.Method == "" || (request.Action != "" && request.Action != "subscribe") {
		http.Error(w, "invalid desktop action", http.StatusBadRequest)
		return
	}
	if request.Method == "native.shellState" || request.Method == "native.shellAction" {
		if !current.shellSettings || request.Action != "" {
			http.Error(w, "desktop settings unavailable", http.StatusForbidden)
			return
		}
		if request.Method == "native.shellAction" {
			if err := d.applyShellSettings(request.Params); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(d.shellSettingsSnapshot())
		return
	}
	if request.Method == "native.pickDirectory" && request.Action == "" {
		path, err := d.app.Dialog.OpenFile().AttachToWindow(current.win).
			CanChooseDirectories(true).CanChooseFiles(false).PromptForSingleSelection()
		if err != nil {
			http.Error(w, "folder selection failed", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(path)
		return
	}
	status := d.core.Status()
	if status.Phase != "running" || status.PID <= 0 {
		http.Error(w, "Orca Core is reconnecting", http.StatusServiceUnavailable)
		return
	}
	if request.Action == "subscribe" {
		d.startDesktopStream(w, current, uint32(status.PID), request.ID, request.Method, request.Params)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if current.desktopContext != nil {
		stop := context.AfterFunc(current.desktopContext, cancel)
		defer stop()
	}
	if d.desktopAccessKey != "" {
		ctx = tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey)
	}
	response, err := tasks.CallLocal(ctx, d.paths.data, uint32(status.PID), request.Method, request.Params)
	if err != nil {
		http.Error(w, "Orca Core request failed", http.StatusBadGateway)
		return
	}
	if d.window.Load() != current || d.core.Status().PID != status.PID {
		http.Error(w, "Orca Core changed during request", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(response)
}

func (d *desktop) startDesktopStream(w http.ResponseWriter, current *orcaWindow, pid uint32, id, method string, params json.RawMessage) {
	if id == "" || current.desktopContext == nil {
		http.Error(w, "invalid desktop subscription", http.StatusBadRequest)
		return
	}
	if current.desktopStreamCount.Add(1) > 24 {
		current.desktopStreamCount.Add(-1)
		http.Error(w, "too many desktop subscriptions", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithCancel(current.desktopContext)
	stream := &desktopStream{frames: make(chan json.RawMessage, 8), cancel: cancel}
	stream.lastPoll.Store(time.Now().UnixNano())
	if _, loaded := current.desktopStreams.LoadOrStore(id, stream); loaded {
		current.desktopStreamCount.Add(-1)
		cancel()
		http.Error(w, "desktop subscription already exists", http.StatusConflict)
		return
	}
	if d.desktopAccessKey != "" {
		ctx = tasks.WithDesktopQuestionAccess(ctx, d.desktopAccessKey)
	}
	local, err := tasks.SubscribeLocal(ctx, d.paths.data, pid, method, params)
	if err != nil {
		current.removeDesktopStream(id, stream)
		http.Error(w, "failed to subscribe to Orca Core", http.StatusBadGateway)
		return
	}
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				if now.Sub(time.Unix(0, stream.lastPoll.Load())) >= 30*time.Second {
					current.removeDesktopStream(id, stream)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		defer local.Close()
		defer close(stream.frames)
		for ctx.Err() == nil {
			frame, err := local.Next()
			if err != nil {
				return
			}
			select {
			case stream.frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	w.WriteHeader(http.StatusNoContent)
}

func (w *orcaWindow) removeDesktopStream(id string, stream *desktopStream) {
	if w.desktopStreams.CompareAndDelete(id, stream) {
		stream.cancel()
		w.desktopStreamCount.Add(-1)
	}
}

func (d *desktop) pollDesktopStream(w http.ResponseWriter, r *http.Request, current *orcaWindow, id string, cancel bool) {
	value, ok := current.desktopStreams.Load(id)
	if !ok {
		w.WriteHeader(http.StatusGone)
		return
	}
	stream := value.(*desktopStream)
	if cancel {
		current.removeDesktopStream(id, stream)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	stream.lastPoll.Store(time.Now().UnixNano())
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	var first json.RawMessage
	select {
	case first, ok = <-stream.frames:
		if !ok {
			current.removeDesktopStream(id, stream)
			w.WriteHeader(http.StatusGone)
			return
		}
	case <-timer.C:
		w.WriteHeader(http.StatusNoContent)
		return
	case <-r.Context().Done():
		return
	}
	frames := []json.RawMessage{first}
	bytes := len(first)
	for bytes < 1<<20 && len(frames) < 8 {
		select {
		case next, open := <-stream.frames:
			if open {
				frames = append(frames, next)
				bytes += len(next)
				continue
			}
		default:
		}
		break
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(frames)
}

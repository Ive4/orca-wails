package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestPipePeerMismatchSendsNoToken(t *testing.T) {
	pid := uint32(os.Getpid()) + 1
	endpoint := fmt.Sprintf(`\\.\pipe\orca-%d-test`, pid)
	listener, err := winio.ListenPipe(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	ready := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ready <- err
			return
		}
		defer conn.Close()
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			ready <- err
			return
		}
		request, err := io.ReadAll(conn)
		if len(request) != 0 {
			ready <- fmt.Errorf("untrusted pipe received %d bytes", len(request))
			return
		}
		ready <- err
	}()

	raw, err := json.Marshal(map[string]any{
		"pid": pid, "runtimeId": "rt_test", "authToken": "test-token",
		"transports": []map[string]string{{"kind": "named-pipe", "endpoint": endpoint}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeMetadata(t, dir, string(raw))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		_, err := Read(ctx, dir, pid)
		// ListenPipe reserves the name before Accept makes it available.
		if errors.Is(err, windows.ERROR_PIPE_BUSY) {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if err == nil || !strings.Contains(err.Error(), "pipe belongs to another process") {
			t.Fatalf("Read error = %v, want pipe PID mismatch", err)
		}
		break
	}
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("test pipe did not see client disconnect")
	}
}

func TestPipeNameRestrictions(t *testing.T) {
	for _, endpoint := range []string{
		`\\server\pipe\orca-42-a`,
		`\\.\pipe\orca-43-a`,
		`\\.\pipe\orca-42-`,
		`\\.\pipe\orca-42-a\b`,
		`\\.\pipe\orca-42-à`,
		`\\.\pipe\orca-42-abcde`,
	} {
		metadata := runtimeMetadata{pid: 42, transports: []transport{{kind: "named-pipe", endpoint: endpoint}}}
		_, err := connectOwned(t.Context(), metadata)
		if err == nil || !(strings.Contains(err.Error(), "expected a local Orca pipe") || strings.Contains(err.Error(), "invalid Orca pipe name")) {
			t.Errorf("connectOwned(%q) error = %v, want pipe name rejection", endpoint, err)
		}
	}
	if !validPipeSuffix("aZ_-") {
		t.Fatal("valid pipe name suffix rejected")
	}
}

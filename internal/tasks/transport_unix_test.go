//go:build darwin || linux

package tasks

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSocketPeerMismatchSendsNoToken(t *testing.T) {
	dir, listener := localSocket(t)
	pid := uint32(os.Getpid()) + 1
	socketMetadata(t, dir, listener, pid)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, err := Read(ctx, dir, pid); err == nil || !strings.Contains(err.Error(), "socket belongs to another process") {
		t.Fatalf("Read error = %v, want socket PID mismatch", err)
	}

	conn := acceptSocket(t, listener)
	request, err := io.ReadAll(conn)
	if err != nil || len(request) != 0 {
		t.Fatalf("unexpected data or error from untrusted socket: %d bytes, %v", len(request), err)
	}
}

func TestCallLocalBindsResponseToOwnedRuntime(t *testing.T) {
	for _, test := range []struct {
		name string
		peer string
	}{
		{"owned", "rt_test"},
		{"different runtime", "other-runtime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, listener := localSocket(t)
			pid := uint32(os.Getpid())
			socketMetadata(t, dir, listener, pid)
			server := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					server <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var request struct{ ID, Method, AuthToken string }
				if err := json.NewDecoder(conn).Decode(&request); err != nil {
					server <- err
					return
				}
				if request.Method != "status.get" || request.AuthToken != "test-token" {
					server <- fmt.Errorf("unexpected local request")
					return
				}
				_, err = fmt.Fprintf(conn, "{\"id\":%q,\"ok\":true,\"_meta\":{\"runtimeId\":%q},\"result\":{}}\n", request.ID, test.peer)
				server <- err
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			frame, err := CallLocal(ctx, dir, pid, "status.get", nil)
			if test.name == "owned" {
				if err != nil || !json.Valid(frame) {
					t.Fatalf("CallLocal = %s, %v", frame, err)
				}
			} else if err == nil {
				t.Fatal("response from the wrong runtime was accepted")
			}
			if err := <-server; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubscribeLocalYieldsAllFramesAndClosesOnCancel(t *testing.T) {
	dir, listener := localSocket(t)
	pid := uint32(os.Getpid())
	socketMetadata(t, dir, listener, pid)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		conn := acceptSocket(t, listener)
		defer conn.Close()
		var request struct {
			ID           string
			WailsDesktop bool
		}
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if json.NewDecoder(conn).Decode(&request) != nil || !request.WailsDesktop {
			return
		}
		for _, line := range []string{"one", "two"} {
			_, _ = fmt.Fprintf(conn, "{\"id\":%q,\"ok\":true,\"_meta\":{\"runtimeId\":\"rt_test\"},\"result\":%q}\n", request.ID, line)
		}
		_, _ = io.Copy(io.Discard, conn)
	}()
	stream, err := SubscribeLocal(ctx, dir, pid, "terminal.subscribe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	for _, want := range []string{"one", "two"} {
		frame, err := stream.Next()
		var result struct{ Result string }
		if err != nil || json.Unmarshal(frame, &result) != nil || result.Result != want {
			t.Fatalf("stream frame = %s, %v, want %q", frame, err, want)
		}
	}
	cancel()
	if _, err := stream.Next(); err != context.Canceled {
		t.Fatalf("stream after cancellation = %v", err)
	}
}

func TestReadSocketProtocol(t *testing.T) {
	dir, listener := localSocket(t)
	pid := uint32(os.Getpid())
	socketMetadata(t, dir, listener, pid)
	done := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			done <- err
			return
		}
		request, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var wire struct {
			ID, AuthToken, Method string
			Params                struct {
				Limit                                    int
				SupportsWorktreeVisibilitySourceDefaults bool
			}
		}
		if err := json.Unmarshal(request, &wire); err != nil {
			done <- err
			return
		}
		if wire.ID != "tray-tasks" || wire.AuthToken != "test-token" || wire.Method != "worktree.ps" ||
			wire.Params.Limit != 256 || !wire.Params.SupportsWorktreeVisibilitySourceDefaults {
			done <- fmt.Errorf("unexpected worktree.ps request")
			return
		}
		_, err = io.WriteString(conn, "{\"_keepalive\":true}\n{\"_keepalive\":true}\n"+
			strings.ReplaceAll(listingFixture, "\n", "")+"\nignored\n")
		done <- err
	}()
	summary, err := Read(t.Context(), dir, pid)
	if err != nil || len(summary.Rows) != 7 {
		t.Fatalf("Read got %d rows, error %v", len(summary.Rows), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReadSocketInterrupted(t *testing.T) {
	dir, listener := localSocket(t)
	pid := uint32(os.Getpid())
	socketMetadata(t, dir, listener, pid)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := Read(ctx, dir, pid)
		done <- err
	}()

	conn := acceptSocket(t, listener)
	if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("Read error = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Read stayed blocked after cancellation")
	}
}

func TestReadSocketRequiresNewlineWithinLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		padding int
		newline bool
		ok      bool
	}{
		{name: "last byte", padding: 8*1024*1024 - 1, newline: true, ok: true},
		{name: "beyond limit", padding: 8 * 1024 * 1024, newline: true},
		{name: "missing newline"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, listener := localSocket(t)
			pid := uint32(os.Getpid())
			socketMetadata(t, dir, listener, pid)
			done := make(chan error, 1)
			go func() {
				conn, err := listener.AcceptUnix()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
					done <- err
					return
				}
				if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
					done <- err
					return
				}
				response := strings.ReplaceAll(listingFixture, "\n", "")
				if tc.padding > len(response) {
					response += strings.Repeat(" ", tc.padding-len(response))
				}
				if tc.newline {
					response += "\n"
				}
				_, err = io.WriteString(conn, response)
				done <- err
			}()
			_, err := Read(t.Context(), dir, pid)
			serverErr := <-done
			if tc.ok && (err != nil || serverErr != nil) {
				t.Fatalf("complete response: client error %v, server error %v", err, serverErr)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "incomplete Orca response")) {
				t.Fatalf("Read error = %v, want incomplete response", err)
			}
		})
	}
}

func TestReadRejectsMetadataSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orca-runtime.json")
	if err := os.Symlink(filepath.Join(t.TempDir(), "runtime.json"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(t.Context(), dir, 43); err == nil || !strings.Contains(err.Error(), "expected local Orca runtime metadata file") {
		t.Fatalf("Read error = %v, want non-regular metadata rejection", err)
	}
}

func localSocket(t *testing.T) (string, *net.UnixListener) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "orca-task-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "runtime.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	if err := listener.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	return dir, listener
}

func socketMetadata(t *testing.T, dir string, listener *net.UnixListener, pid uint32) {
	t.Helper()
	writeMetadata(t, dir, fmt.Sprintf(`{"pid":%d,"runtimeId":"rt_test","authToken":"test-token","transports":[{"kind":"unix","endpoint":%q}]}`, pid, listener.Addr().String()))
}

func acceptSocket(t *testing.T, listener *net.UnixListener) *net.UnixConn {
	t.Helper()
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return conn
}

package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const pairingSecret = "PAIRED_FIXTURE_VALUE"

func TestOwnedCoreLifecycle(t *testing.T) {
	var m Manager
	defer m.EmergencyStop()
	spec := mockSpec(t, "ready")
	ctx := context.Background()
	assertPhase(t, &m, "stopped")
	if err := m.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	status := assertPhase(t, &m, "running")
	if status.PID <= 0 || status.Port != spec.Port || status.Version != spec.Version {
		t.Fatalf("unexpected status: %+v", status)
	}
	url, port, ok := m.Offer()
	if !ok || port != spec.Port || !strings.HasSuffix(url, pairingSecret) {
		t.Fatal("missing verified pairing offer")
	}
	data, err := json.Marshal(status)
	var fields map[string]any
	if err != nil || json.Unmarshal(data, &fields) != nil || len(fields) != 5 || fields["phase"] != "running" ||
		fields["pid"] == nil || fields["port"] == nil || fields["version"] == nil || !strings.Contains(string(data), `"detail":`) ||
		strings.Contains(string(data), pairingSecret) {
		t.Fatal("unexpected status JSON fields")
	}
	if err := m.Start(ctx, spec); err != nil || m.Status().PID != status.PID {
		t.Fatalf("same spec did not reuse its owned process: %v", err)
	}
	different := spec
	different.Version = "v9"
	if err := m.Start(ctx, different); err == nil || m.Status().PID != status.PID {
		t.Fatal("start replaced a running core")
	}
	leaf := leafAddress(t, spec)
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	assertPhase(t, &m, "stopped")
	noOffer(t, &m)
	assertPortFree(t, leaf)
	assertPortFree(t, specAddress(spec))
	stdout, err := os.ReadFile(filepath.Join(spec.LogDir, "core.stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.ReadFile(filepath.Join(spec.LogDir, "core.stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stdout)+string(stderr), pairingSecret) ||
		!strings.Contains(string(stdout), "pairing credentials omitted") ||
		!strings.Contains(string(stderr), "http://127.0.0.1/diagnostic") ||
		strings.Contains(string(stderr), "private-user") || strings.Contains(string(stderr), "blocked-host-hook") {
		t.Fatal("logs did not preserve only safe diagnostics")
	}
	if !strings.Contains(string(stderr), "explicit-node-options survived") {
		t.Fatal("explicit host-environment override was lost")
	}
}

func TestOccupiedPortIsNeverAdopted(t *testing.T) {
	var m Manager
	spec := mockSpec(t, "ready")
	l, err := net.Listen("tcp4", specAddress(spec))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := m.Start(context.Background(), spec); !errors.Is(err, ErrPortOccupied) {
		t.Fatalf("expected ErrPortOccupied, got %v", err)
	}
	noOffer(t, &m)
	assertPhase(t, &m, "stopped")
	if _, err := os.Stat(filepath.Join(spec.Dir, "leaf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("spawned a process for an occupied port")
	}
}

func TestSpawnFailureLeavesManagerReusable(t *testing.T) {
	var m Manager
	defer m.EmergencyStop()
	spec := mockSpec(t, "ready")
	broken := spec
	broken.Program = filepath.Join(spec.Dir, "broken.exe")
	if err := os.WriteFile(broken.Program, []byte("invalid executable"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Start(ctx, broken); err == nil {
		t.Fatal("invalid executable started")
	}
	assertPhase(t, &m, "failed")
	noOffer(t, &m)
	if err := m.Start(ctx, spec); err != nil {
		t.Fatalf("could not start after a spawn failure: %v", err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidReadinessCleansTreeBeforeRetry(t *testing.T) {
	for _, mode := range []string{"pid", "schema", "version", "endpoint", "self-test", "offer"} {
		t.Run(mode, func(t *testing.T) {
			var m Manager
			defer m.EmergencyStop()
			spec := mockSpec(t, mode)
			ctx := context.Background()
			if err := m.Start(ctx, spec); err == nil || errors.Is(err, context.DeadlineExceeded) ||
				strings.Contains(err.Error(), pairingSecret) {
				t.Fatal("expected immediate, redacted rejection of the readiness record")
			}
			assertPhase(t, &m, "failed")
			noOffer(t, &m)
			assertPortFree(t, leafAddress(t, spec))
			spec.Env["ORCA_CORE_TEST_ROLE"] = "ready"
			if err := m.Start(ctx, spec); err != nil {
				t.Fatalf("same-port rollback failed: %v", err)
			}
			if err := m.Stop(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHealthNeedsOwnedReadinessAndDoesNotFollowRedirects(t *testing.T) {
	for _, mode := range []string{"no-record", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			var m Manager
			defer m.EmergencyStop()
			spec := mockSpec(t, mode)
			spec.StartupTimeout = 850 * time.Millisecond
			if err := m.Start(context.Background(), spec); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected a readiness timeout, got %v", err)
			}
			assertPhase(t, &m, "failed")
			noOffer(t, &m)
			assertPortFree(t, leafAddress(t, spec))
			assertPortFree(t, specAddress(spec))
		})
	}
}

func TestPairingUnavailableStillRuns(t *testing.T) {
	var m Manager
	spec := mockSpec(t, "unpaired")
	defer m.EmergencyStop()
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if assertPhase(t, &m, "running").Detail == "" {
		t.Fatal("missing explanation for unavailable pairing")
	}
	noOffer(t, &m)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledOrEmergencyStartupNeverPublishesOffer(t *testing.T) {
	for _, emergency := range []bool{false, true} {
		t.Run(fmt.Sprint(emergency), func(t *testing.T) {
			var m Manager
			spec := mockSpec(t, "stall-health")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			defer m.EmergencyStop()
			result := make(chan error, 1)
			go func() { result <- m.Start(ctx, spec) }()
			waitFor(t, func() bool {
				_, err := os.Stat(filepath.Join(spec.Dir, "health-requested"))
				return err == nil
			})
			canceledCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancelWait()
			if err := m.Stop(canceledCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("queued stop did not respect its deadline: %v", err)
			}
			if emergency {
				m.EmergencyStop()
			} else {
				cancel()
			}
			select {
			case err := <-result:
				if err == nil || (!emergency && !errors.Is(err, context.Canceled)) {
					t.Fatalf("unexpected startup result: %v", err)
				}
			case <-time.After(6 * time.Second):
				t.Fatal("startup did not finish")
			}
			noOffer(t, &m)
			if emergency {
				assertPhase(t, &m, "stopped")
			} else {
				assertPhase(t, &m, "failed")
			}
			spec.Env["ORCA_CORE_TEST_ROLE"] = "ready"
			if err := m.Start(context.Background(), spec); err != nil {
				t.Fatalf("restart after interrupted startup: %v", err)
			}
			if err := m.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUnexpectedCoreExitRevokesOfferAndCleansDescendant(t *testing.T) {
	var m Manager
	spec := mockSpec(t, "ready")
	ctx := context.Background()
	defer m.EmergencyStop()
	if err := m.Start(ctx, spec); err != nil {
		t.Fatal(err)
	}
	addr := leafAddress(t, spec)
	response, err := http.Get(spec.HealthURL + "exit")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	waitFor(t, func() bool {
		l, err := net.Listen("tcp4", addr)
		if err != nil {
			return false
		}
		l.Close()
		return true
	})
	if status := assertPhase(t, &m, "failed"); status.PID != 0 || status.Detail == "" {
		t.Fatalf("unexpected crash status: %+v", status)
	}
	noOffer(t, &m)
	if err := m.Start(ctx, spec); err != nil {
		t.Fatalf("could not reuse port after unexpected exit: %v", err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func mockSpec(t *testing.T, role string) Spec {
	t.Helper()
	t.Setenv("ELECTRON_RUN_AS_NODE", "blocked-host-hook")
	t.Setenv("ORCA_APP_EXECUTABLE", "blocked-host-hook")
	t.Setenv("NODE_OPTIONS", "blocked-host-hook")
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return runtimeSpec(program, t.TempDir(), port, role)
}

func runtimeSpec(program, dir string, port uint16, role string) Spec {
	return Spec{
		Program: program, Args: []string{"-test.run=^TestMockCoreRuntime$"}, Dir: dir, Port: port,
		HealthURL: fmt.Sprintf("http://127.0.0.1:%d/", port), LogDir: dir, Version: "v1.4.264",
		StartupTimeout: 5 * time.Second, ShutdownTimeout: 250 * time.Millisecond,
		Env: map[string]string{
			"ORCA_CORE_TEST_ROLE": role, "ORCA_CORE_TEST_PORT": strconv.Itoa(int(port)),
			"NODE_OPTIONS": "explicit-node-options",
		},
	}
}

// TestMockCoreRuntime runs in the test executable launched by the real supervisor.
func TestMockCoreRuntime(t *testing.T) {
	role := os.Getenv("ORCA_CORE_TEST_ROLE")
	switch role {
	case "":
		return
	case "leaf":
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("leaf", []byte(listener.Addr().String()), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			conn, err := listener.Accept()
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
		}
	}
	address := "127.0.0.1:" + os.Getenv("ORCA_CORE_TEST_PORT")
	if role == "hold-stop" {
		signal.Ignore(syscall.SIGTERM)
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove("leaf"); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	leaf := exec.Command(program, "-test.run=^TestMockCoreRuntime$")
	leaf.Env = append(os.Environ(), "ORCA_CORE_TEST_ROLE=leaf")
	if err := leaf.Start(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		data, err := os.ReadFile("leaf")
		return err == nil && len(data) > 0
	})
	format := "http://" + address + "/#pairing=" + pairingSecret
	record := map[string]any{
		"type": "orca_server_ready", "schemaVersion": 1, "boundEndpoint": "ws://" + address,
		"health": map[string]any{"pid": os.Getpid(), "buildVersion": "1.4.264",
			"terminalDaemon": map[string]any{"selfTest": map[string]bool{"ok": role != "self-test"}}},
		"pairing": map[string]any{"available": role != "unpaired", "webClientUrl": format},
	}
	switch role {
	case "pid":
		record["health"].(map[string]any)["pid"] = os.Getppid()
	case "version":
		record["health"].(map[string]any)["buildVersion"] = "1.4.263"
	case "schema":
		record["schemaVersion"] = 2
	case "endpoint":
		record["boundEndpoint"] = "ws://example.test:443/" + pairingSecret
	case "offer":
		record["pairing"].(map[string]any)["webClientUrl"] = "http://example.test/#" + pairingSecret
	}
	fmt.Fprintln(os.Stdout, "raw stdout", pairingSecret)
	fmt.Fprintln(os.Stderr, "pairing offer", pairingSecret)
	fmt.Fprintln(os.Stderr, "diagnostic at HTTP://private-user:private-pass@127.0.0.1/diagnostic?x="+pairingSecret+"#fragment")
	for _, key := range []string{"ELECTRON_RUN_AS_NODE", "ORCA_APP_EXECUTABLE", "NODE_OPTIONS"} {
		fmt.Fprintln(os.Stderr, os.Getenv(key))
	}
	fmt.Fprintln(os.Stderr, os.Getenv("NODE_OPTIONS"), "survived")
	if role != "no-record" {
		if err := json.NewEncoder(os.Stdout).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if role == "stall-health" {
			if err := os.WriteFile("health-requested", nil, 0600); err != nil {
				http.Error(w, "could not record request", http.StatusInternalServerError)
				return
			}
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/exit" {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			os.Exit(7)
		}
		if role == "redirect" && r.URL.Path == "/" {
			http.Redirect(w, r, "/healthy", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})); err != nil {
		t.Fatal(err)
	}
}

func specAddress(spec Spec) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Port)))
}

func leafAddress(t *testing.T, spec Spec) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(spec.Dir, "leaf"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertPhase(t *testing.T, m *Manager, want string) Status {
	t.Helper()
	status := m.Status()
	if string(status.Phase) != want {
		t.Fatalf("got status %+v, want phase %s", status, want)
	}
	return status
}

func noOffer(t *testing.T, m *Manager) {
	t.Helper()
	if _, _, ok := m.Offer(); ok {
		t.Fatal("published an offer without a running, paired core")
	}
}

func assertPortFree(t *testing.T, addr string) {
	t.Helper()
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatalf("owned port was still occupied: %v", err)
	}
	l.Close()
}

func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

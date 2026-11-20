//go:build unix

package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCanceledGracefulStopStillTerminatesOwnedTree(t *testing.T) {
	var m Manager
	defer m.EmergencyStop()
	spec := mockSpec(t, "hold-stop")
	spec.ShutdownTimeout = 5 * time.Second
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	leaf := leafAddress(t, spec)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- m.Stop(ctx) }()
	waitFor(t, func() bool { return m.Status().Phase == "stopping" })
	noOffer(t, &m)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stop did not report the canceled grace period: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stop remained blocked in its grace period")
	}
	spec.Env["ORCA_CORE_TEST_ROLE"] = "ready"
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatalf("restart before interrupted stop was cleaned up: %v", err)
	}
	assertPortFree(t, leaf)
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

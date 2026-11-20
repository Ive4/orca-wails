package core

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestWindowsJobClosesWhenOwnerDies(t *testing.T) {
	spec := mockSpec(t, "ready")
	owner := exec.Command(spec.Program, "-test.run=^TestWindowsJobOwnerHelper$")
	owner.Dir = spec.Dir
	owner.Env = append(os.Environ(), "ORCA_CORE_TEST_OWNER_PORT="+strconv.Itoa(int(spec.Port)))
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if owner.ProcessState == nil {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	})
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(spec.Dir, "owner-ready"))
		return err == nil
	})
	leaf := leafAddress(t, spec)
	if err := owner.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.Wait()
	waitFor(t, func() bool {
		for _, addr := range []string{specAddress(spec), leaf} {
			l, err := net.Listen("tcp4", addr)
			if err != nil {
				return false
			}
			l.Close()
		}
		return true
	})
}

func TestWindowsJobOwnerHelper(t *testing.T) {
	number := os.Getenv("ORCA_CORE_TEST_OWNER_PORT")
	if number == "" {
		return
	}
	port, err := strconv.ParseUint(number, 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	spec := runtimeSpec(program, dir, uint16(port), "ready")
	var m Manager
	if err := m.Start(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("owner-ready", nil, 0600); err != nil {
		t.Fatal(err)
	}
	// The parent kills this owner without running EmergencyStop.
	for {
		time.Sleep(time.Second)
	}
}

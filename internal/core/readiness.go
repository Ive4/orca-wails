package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func validateSpec(spec Spec) (string, error) {
	program, err := os.Stat(spec.Program)
	if !filepath.IsAbs(spec.Program) || err != nil || !program.Mode().IsRegular() {
		return "", errors.New("core program must be an existing absolute file path")
	}
	dir, err := os.Stat(spec.Dir)
	if !filepath.IsAbs(spec.Dir) || err != nil || !dir.IsDir() {
		return "", errors.New("core working directory must exist and be absolute")
	}
	if spec.Port == 0 {
		return "", errors.New("core port must be nonzero")
	}
	if spec.StartupTimeout <= 0 {
		return "", errors.New("core startup timeout must be positive")
	}
	host, err := loopbackHost(spec.HealthURL, "http", spec.Port)
	if err != nil {
		return "", err
	}
	for key, value := range spec.Env {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
			return "", errors.New("invalid core environment entry")
		}
	}
	for _, arg := range spec.Args {
		if strings.ContainsRune(arg, 0) {
			return "", errors.New("core arguments cannot contain NUL")
		}
	}
	return net.JoinHostPort(host, strconv.Itoa(int(spec.Port))), nil
}

func loopbackHost(value, scheme string, port uint16) (string, error) {
	u, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid core URL")
	}
	if u.Scheme != scheme {
		return "", errors.New("core URL uses an unexpected transport")
	}
	host, err := netip.ParseAddr(u.Hostname())
	if err != nil || (host.String() != "127.0.0.1" && host != netip.IPv6Loopback()) {
		return "", errors.New("core URL must use a literal loopback address")
	}
	urlPort := u.Port()
	if urlPort == "" {
		urlPort = "80"
	}
	actualPort, err := strconv.ParseUint(urlPort, 10, 16)
	if err != nil || actualPort != uint64(port) {
		return "", errors.New("core URL port does not match the configured port")
	}
	if u.User != nil {
		return "", errors.New("core URL cannot contain user credentials")
	}
	return host.String(), nil
}

type readinessEvent map[string]any

func decodeReadiness(line []byte) readinessEvent {
	if !json.Valid(line) || !utf8.Valid(line) {
		return nil
	}
	var event readinessEvent
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&event); err != nil || event["type"] != "orca_server_ready" {
		return nil
	}
	return event
}

func (event readinessEvent) verify(spec Spec, pid int) (offer, detail string, err error) {
	if event["schemaVersion"] != json.Number("1") {
		return "", "", errors.New("unsupported Orca readiness schema")
	}
	endpoint, ok := event["boundEndpoint"].(string)
	if !ok {
		return "", "", errors.New("Orca did not publish a bound WebSocket endpoint")
	}
	if _, err := loopbackHost(endpoint, "ws", spec.Port); err != nil {
		return "", "", err
	}
	health, _ := event["health"].(map[string]any)
	version, ok := health["buildVersion"].(string)
	if !ok || strings.TrimLeft(version, "v") != strings.TrimLeft(spec.Version, "v") {
		return "", "", errors.New("running Orca version does not match the selected package")
	}
	if health["pid"] != json.Number(strconv.Itoa(pid)) {
		return "", "", errors.New("Orca health PID does not match the owned process")
	}
	daemon, _ := health["terminalDaemon"].(map[string]any)
	test, _ := daemon["selfTest"].(map[string]any)
	if test["ok"] != true {
		return "", "", errors.New("Orca terminal daemon self-test failed or was not reported")
	}
	pairing, _ := event["pairing"].(map[string]any)
	available, ok := pairing["available"].(bool)
	if !ok {
		return "", "", errors.New("Orca pairing availability is invalid")
	}
	if !available {
		return "", "Core is ready, but pairing is unavailable. Review the core pairing configuration before opening a window.", nil
	}
	offer, ok = pairing["webClientUrl"].(string)
	if !ok {
		return "", "", errors.New("Orca did not provide a paired Web client URL")
	}
	if _, err := loopbackHost(offer, "http", spec.Port); err != nil {
		return "", "", err
	}
	return offer, "", nil
}

func awaitReady(ctx context.Context, spec Spec, pid int, events <-chan readinessEvent) (offer, detail string, err error) {
	select {
	case <-ctx.Done():
		return "", "", readinessContextError(ctx)
	case event := <-events:
		offer, detail, err = event.verify(spec, pid)
		if err != nil {
			return "", "", err
		}
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if healthy(ctx, spec.HealthURL) {
			return offer, detail, nil
		}
		select {
		case <-ctx.Done():
			return "", "", readinessContextError(ctx)
		case <-ticker.C:
		}
	}
}

func readinessContextError(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("core readiness timed out; check core.stderr.log: %w", context.DeadlineExceeded)
	}
	return ctx.Err()
}

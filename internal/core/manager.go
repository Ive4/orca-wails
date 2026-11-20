// Package core supervises a foreground Orca runtime owned by the desktop app.
package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Spec describes how to launch a local, pinned Orca runtime.
type Spec struct {
	Program                         string
	Args                            []string
	Dir                             string
	Env                             map[string]string
	Port                            uint16
	HealthURL, LogDir, Version      string
	StartupTimeout, ShutdownTimeout time.Duration
}

// Status is safe to serialize. Pairing credentials are available only through Offer.
type Status struct {
	Phase   string `json:"phase"`
	PID     int    `json:"pid"`
	Port    uint16 `json:"port"`
	Version string `json:"version"`
	Detail  string `json:"detail"`
}

const (
	stopped  = "stopped"
	starting = "starting"
	running  = "running"
	stopping = "stopping"
	failed   = "failed"

	cleanupTimeout = 5 * time.Second
	pollInterval   = 25 * time.Millisecond
)

var (
	// ErrPortOccupied indicates that a pre-existing listener prevented launch.
	ErrPortOccupied = errors.New("core port is occupied")
	errInterrupted  = errors.New("core startup was interrupted")
	healthClient    = &http.Client{
		Transport: &http.Transport{
			DialContext:       (&net.Dialer{Timeout: time.Second}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       2 * time.Second,
	}
)

// Manager owns at most one runtime. Its zero value is ready to use.
type Manager struct {
	mu         sync.Mutex
	initOp     sync.Once
	operation  chan struct{}
	generation uint64
	active     *run
	status     Status
	offer      string
}

// All native process operations run under Manager.mu.
type run struct {
	process         *platformProcess
	program         string
	healthURL       string
	shutdownTimeout time.Duration
	cancelStartup   context.CancelFunc
	stdout, stderr  *os.File
	drained         <-chan struct{}
	done            chan struct{}
	drainUntil      time.Time
	treeGone        bool
	unexpectedExit  bool
}

// Status returns the last observed lifecycle state.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observeExit()
	status := m.status
	if status.Phase == "" {
		status.Phase = stopped
	}
	return status
}

// Offer returns a secret-bearing URL for native WebView navigation.
func (m *Manager) Offer() (string, uint16, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.observeExit()
	if m.status.Phase != running || m.offer == "" {
		return "", 0, false
	}
	return m.offer, m.status.Port, true
}

func (m *Manager) acquire(ctx context.Context) (func(), error) {
	m.initOp.Do(func() {
		m.operation = make(chan struct{}, 1)
	})
	op := m.operation
	select {
	case op <- struct{}{}:
		release := func() { <-op }
		if err := ctx.Err(); err != nil {
			release()
			return nil, err
		}
		return release, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Start waits for owned stdout readiness and a successful loopback health check.
func (m *Manager) Start(ctx context.Context, spec Spec) (err error) {
	release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	m.observeExit()
	previous, generation := m.active, m.generation
	if m.status.Phase == running {
		same := previous.program == spec.Program && previous.healthURL == spec.HealthURL && m.status.Version == spec.Version
		m.mu.Unlock()
		if same {
			return nil
		}
		return errors.New("a different core is already running; stop it first")
	}
	m.mu.Unlock()

	if previous != nil {
		if err := m.reap(ctx, previous); err != nil {
			return fmt.Errorf("previous core tree has not exited: %w", err)
		}
	}
	address, err := validateSpec(spec)
	if err != nil {
		return err
	}
	if err := probePort(ctx, address, spec.Port); err != nil {
		return err
	}

	m.mu.Lock()
	if err := m.checkStart(ctx, generation); err != nil {
		m.mu.Unlock()
		return err
	}
	m.status = Status{Phase: starting, Version: spec.Version, Port: spec.Port}
	m.offer = ""
	m.mu.Unlock()

	var p *run
	defer func() {
		if err == nil {
			return
		}
		m.mu.Lock()
		if m.generation == generation && m.status.Phase == starting {
			m.status.Phase, m.status.PID = failed, 0
			m.status.Detail = "Core startup failed or was cancelled; inspect the returned error and core logs"
		}
		m.mu.Unlock()
		if p != nil {
			if cleanupErr := m.reap(context.Background(), p); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("core tree is still terminating: %w", cleanupErr))
			}
		}
	}()

	startCtx, cancel := context.WithTimeout(ctx, spec.StartupTimeout)
	defer cancel()
	p, events, err := m.launch(startCtx, cancel, generation, spec)
	if err != nil {
		return err
	}
	offer, detail, err := awaitReady(startCtx, spec, p.process.id(), events)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, context.Canceled) {
			return m.startInterrupted(generation)
		}
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.observeExit()
	if err := m.checkStart(ctx, generation); err != nil {
		return err
	}
	if m.status.Phase != starting {
		return errors.New("core exited during readiness verification")
	}
	if err := startCtx.Err(); err != nil {
		return err
	}
	m.status.Phase, m.status.Detail = running, detail
	m.offer = offer
	return nil
}

func (m *Manager) checkStart(ctx context.Context, generation uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if generation != m.generation {
		return errInterrupted
	}
	return nil
}

func (m *Manager) startInterrupted(generation uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation == generation && m.status.Phase == failed && m.status.Detail != "" {
		return errors.New(m.status.Detail)
	}
	return errInterrupted
}

func (m *Manager) launch(ctx context.Context, cancel context.CancelFunc, generation uint64, spec Spec) (*run, <-chan readinessEvent, error) {
	pipes, err := prepareOutput(spec.LogDir)
	if err != nil {
		return nil, nil, err
	}
	defer pipes.closeChildEnds()

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkStart(ctx, generation); err != nil {
		pipes.closeParentEnds()
		return nil, nil, err
	}
	process, err := spawn(spec, pipes.stdin, pipes.stdoutWriter, pipes.stderrWriter)
	if err != nil {
		pipes.closeParentEnds()
		return nil, nil, fmt.Errorf("could not launch the owned core: %w", err)
	}
	events := make(chan readinessEvent, 1)
	p := &run{
		process: process, program: spec.Program, healthURL: spec.HealthURL, shutdownTimeout: spec.ShutdownTimeout,
		cancelStartup: cancel, stdout: pipes.stdoutReader, stderr: pipes.stderrReader,
		drained: pipes.drain(events), done: make(chan struct{}),
	}
	m.active, m.status.PID = p, process.id()
	go m.watch(p)
	return p, events, nil
}

// Stop requests graceful shutdown where available, then terminates and reaps the owned tree.
func (m *Manager) Stop(ctx context.Context) error {
	release, err := m.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	m.mu.Lock()
	m.observeExit()
	p := m.active
	if p == nil {
		m.setStopped()
		m.mu.Unlock()
		return nil
	}
	generation := m.generation
	wasRunning := m.status.Phase == running
	m.status.Phase, m.offer = stopping, ""
	var graceful bool
	var gracefulErr error
	if wasRunning && ctx.Err() == nil {
		graceful, gracefulErr = p.process.requestGracefulStop()
	}
	m.mu.Unlock()

	if graceful && gracefulErr == nil {
		gracefulErr = m.waitForGrace(ctx, p)
	}
	m.mu.Lock()
	if m.active == p {
		err = p.process.killTree()
	}
	m.mu.Unlock()
	if err == nil {
		err = m.reap(ctx, p)
	}
	err = errors.Join(gracefulErr, err)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.generation == generation {
		if err == nil {
			m.setStopped()
		} else {
			m.status.Phase, m.status.PID = failed, 0
			m.status.Detail = "Core stop was interrupted; the owned tree is terminating"
		}
	}
	return err
}

func (m *Manager) waitForGrace(ctx context.Context, p *run) error {
	deadline := time.NewTimer(p.shutdownTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		if m.active != p || m.status.Phase != stopping {
			m.mu.Unlock()
			return nil
		}
		exited, err := p.process.leaderExited()
		m.mu.Unlock()
		if exited || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return nil
		case <-ticker.C:
		}
	}
}

// EmergencyStop revokes the offer and immediately signals the owned tree.
// Cleanup remains tracked so a later Start can wait before reusing the port.
func (m *Manager) EmergencyStop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	if p := m.active; p != nil {
		p.cancelStartup()
		_ = p.process.killTree()
	}
	m.setStopped()
}

func (m *Manager) setStopped() {
	m.status.Phase, m.status.PID, m.status.Detail = stopped, 0, ""
	m.offer = ""
}

func (m *Manager) watch(p *run) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.done:
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		if m.active == p {
			m.observeExit()
			if m.status.Phase == failed || m.status.Phase == stopped {
				_, _ = m.collect(p)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Manager) observeExit() {
	p := m.active
	if p == nil || (m.status.Phase != starting && m.status.Phase != running) {
		return
	}
	exited, err := p.process.leaderExited()
	if !exited && err == nil {
		return
	}
	detail := "Core exited unexpectedly"
	if err != nil {
		detail = "Could not query core process state"
	}
	p.unexpectedExit = exited
	_ = p.process.killTree()
	m.status.Phase, m.status.PID, m.status.Detail = failed, 0, detail
	m.offer = ""
	p.cancelStartup()
}

func (m *Manager) reap(ctx context.Context, p *run) error {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		done, err := m.collect(p)
		m.mu.Unlock()
		if done || err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Manager) collect(p *run) (bool, error) {
	if m.active != p {
		return true, nil
	}
	if !p.treeGone {
		if err := p.process.killTree(); err != nil {
			return false, err
		}
		gone, err := p.process.treeEmpty()
		if err != nil || !gone {
			return false, err
		}
		p.treeGone = true
		p.drainUntil = time.Now().Add(100 * time.Millisecond)
	}
	select {
	case <-p.drained:
	default:
		// A descendant outside the process group may still hold a pipe writer.
		if time.Now().After(p.drainUntil) {
			p.stdout.Close()
			p.stderr.Close()
		}
		return false, nil
	}
	if m.status.Phase == failed && p.unexpectedExit {
		if detail := p.process.exitDetail(); detail != "" {
			m.status.Detail = "Core exited: " + detail
		}
	}
	p.process.close()
	m.active = nil
	close(p.done)
	return true, nil
}

func probePort(ctx context.Context, addr string, port uint16) error {
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addr)
	if isPortOccupied(err) {
		return fmt.Errorf("port %d: %w", port, ErrPortOccupied)
	}
	if err != nil {
		return fmt.Errorf("cannot bind core port %d: %w", port, err)
	}
	return l.Close()
}

func healthy(ctx context.Context, url string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	response, err := healthClient.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

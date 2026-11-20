//go:build unix

package core

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// A private Unix process group owns descendants that stay in the group.
type platformProcess struct {
	leader   *os.Process
	pid      int
	killSent bool
	reaped   bool
	exit     string
}

func spawn(spec Spec, stdin, stdout, stderr *os.File) (*platformProcess, error) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	configureParentDeath(attr)
	process, err := os.StartProcess(spec.Program, append([]string{spec.Program}, spec.Args...), &os.ProcAttr{
		Dir:   spec.Dir,
		Env:   runtimeEnvironment(spec.Env, false),
		Files: []*os.File{stdin, stdout, stderr},
		Sys:   attr,
	})
	if err != nil {
		return nil, err
	}
	return &platformProcess{leader: process, pid: process.Pid}, nil
}

func isPortOccupied(err error) bool { return errors.Is(err, unix.EADDRINUSE) }

func newOutputPipe() (*os.File, *os.File, error) { return os.Pipe() }

func (p *platformProcess) id() int { return p.pid }

func (p *platformProcess) requestGracefulStop() (bool, error) {
	if p.killSent {
		return false, nil
	}
	return true, p.signalGroup(unix.SIGTERM)
}

func (p *platformProcess) killTree() error {
	if p.killSent {
		return nil
	}
	if err := p.signalGroup(unix.SIGKILL); err != nil {
		// Darwin can reject a signal to a group whose members are all zombies.
		if !errors.Is(err, unix.EPERM) {
			return err
		}
		if empty, checkErr := groupEmpty(p.id()); checkErr != nil || !empty {
			return err
		}
	}
	p.killSent = true
	return nil
}

func (p *platformProcess) signalGroup(signal unix.Signal) error {
	err := unix.Kill(-p.id(), signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	return err
}

func (p *platformProcess) treeEmpty() (bool, error) {
	exited, err := p.leaderExited()
	if err != nil || !exited {
		return false, err
	}
	empty, err := groupEmpty(p.id())
	if err != nil {
		return false, err
	}
	if !empty {
		if !p.reaped {
			// A group member may have forked while the original signal was delivered.
			_ = p.signalGroup(unix.SIGKILL)
		}
		return false, nil
	}
	// Keep the exited leader's PID reserved until the group is gone.
	if !p.reaped {
		state, err := p.leader.Wait()
		if err != nil {
			return false, err
		}
		p.reaped, p.exit = true, state.String()
	}
	return true, nil
}

func (p *platformProcess) exitDetail() string { return p.exit }

func (p *platformProcess) close() { _ = p.leader.Release() }

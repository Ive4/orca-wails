//go:build unix && !darwin && !linux

package core

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func configureParentDeath(*syscall.SysProcAttr) {}

func (p *platformProcess) leaderExited() (bool, error) {
	if p.reaped {
		return true, nil
	}
	var status unix.WaitStatus
	pid, err := unix.Wait4(p.id(), &status, unix.WNOHANG, nil)
	if errors.Is(err, unix.EINTR) || (err == nil && pid == 0) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	p.reaped = true
	if status.Signaled() {
		p.exit = fmt.Sprintf("signal: %s", status.Signal())
	} else {
		p.exit = fmt.Sprintf("exit status %d", status.ExitStatus())
	}
	return true, nil
}

func groupEmpty(group int) (bool, error) {
	err := unix.Kill(-group, 0)
	if errors.Is(err, unix.ESRCH) {
		return true, nil
	}
	return false, err
}

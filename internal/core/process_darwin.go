package core

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// SZOMB marks a process that exited and is awaiting collection by its parent.
const zombie = 5

func configureParentDeath(*syscall.SysProcAttr) {}

func (p *platformProcess) leaderExited() (bool, error) {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.pid", p.id())
	if err != nil {
		return false, err
	}
	return len(list) == 0 || list[0].Proc.P_stat == zombie, nil
}

func groupEmpty(group int) (bool, error) {
	list, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", group)
	if err != nil {
		return false, err
	}
	for _, process := range list {
		if process.Proc.P_stat != zombie {
			return false, nil
		}
	}
	return true, nil
}

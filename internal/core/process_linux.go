package core

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func configureParentDeath(attr *syscall.SysProcAttr) {
	// Go also checks for parent death in the child before exec.
	attr.Pdeathsig = unix.SIGKILL
}

func (p *platformProcess) leaderExited() (bool, error) {
	for {
		var info unix.Siginfo
		err := unix.Waitid(unix.P_PID, p.id(), &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		return info.Signo != 0, err
	}
}

func groupEmpty(group int) (bool, error) {
	// An orphan may remain a zombie under a supervisor that does not reap.
	// It can no longer execute or hold a socket, even if kill(0) sees it.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	want := strconv.Itoa(group)
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ESRCH) {
			continue
		}
		if err != nil {
			return false, err
		}
		stat := string(data)
		if end := strings.LastIndex(stat, ") "); end >= 0 {
			fields := strings.Fields(stat[end+2:])
			if len(fields) >= 3 && fields[2] == want && fields[0] != "Z" && fields[0] != "X" {
				return false, nil
			}
		}
	}
	return true, nil
}

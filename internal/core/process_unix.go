//go:build unix

package core

import (
	"errors"
	"os"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const questionKeyFDEnv = "ORCA_WAILS_QUESTION_KEY_FD"

// A private Unix process group owns descendants that stay in the group.
type platformProcess struct {
	leader   *os.Process
	pid      int
	killSent bool
	reaped   bool
	exit     string
}

func spawn(spec Spec, stdin, stdout, stderr *os.File) (*platformProcess, error) {
	env := slices.DeleteFunc(runtimeEnvironment(spec.Env, false), func(entry string) bool {
		return strings.HasPrefix(entry, questionKeyFDEnv+"=")
	})
	files := []*os.File{stdin, stdout, stderr}
	if key := spec.DesktopQuestionKey; key != "" {
		if len(key) != 64 || strings.IndexFunc(key, func(c rune) bool {
			return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f')
		}) >= 0 {
			return nil, errors.New("invalid desktop question key")
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			return nil, errors.New("could not create desktop question key pipe")
		}
		defer reader.Close()
		_, writeErr := writer.WriteString(key)
		closeErr := writer.Close()
		if writeErr != nil || closeErr != nil {
			return nil, errors.New("could not prepare desktop question key pipe")
		}
		files = append(files, reader)
		env = append(env, questionKeyFDEnv+"=3")
	}

	attr := &syscall.SysProcAttr{Setpgid: true}
	configureParentDeath(attr)
	process, err := os.StartProcess(spec.Program, append([]string{spec.Program}, spec.Args...), &os.ProcAttr{
		Dir:   spec.Dir,
		Env:   env,
		Files: files,
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

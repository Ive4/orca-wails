package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// PROC_THREAD_ATTRIBUTE_JOB_LIST from the Windows SDK. Enrollment happens in
// CreateProcess, so a crash cannot orphan a child between creation and assignment.
const procThreadAttributeJobList = 0x0002000d

type platformProcess struct {
	job, process windows.Handle
	pid          int
	killSent     bool
}

func spawn(spec Spec, stdin, stdout, stderr *os.File) (*platformProcess, error) {
	if !strings.EqualFold(filepath.Ext(spec.Program), ".exe") {
		return nil, errors.New("Windows core entry must be a native .exe")
	}
	program, err := syscall.UTF16PtrFromString(spec.Program)
	if err != nil {
		return nil, err
	}
	dir, err := syscall.UTF16PtrFromString(spec.Dir)
	if err != nil {
		return nil, err
	}
	commandLine, err := syscall.UTF16FromString(windows.ComposeCommandLine(append([]string{spec.Program}, spec.Args...)))
	if err != nil {
		return nil, err
	}
	if len(commandLine) > 32767 {
		return nil, errors.New("core command line exceeds the Windows limit")
	}
	env, err := windowsEnvironment(spec.Env)
	if err != nil {
		return nil, err
	}

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	// The x/sys Job APIs accept uintptr, so pin their output and input buffers.
	var pinned runtime.Pinner
	pinned.Pin(&limits)
	defer pinned.Unpin()
	_, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	if err != nil {
		return nil, fmt.Errorf("could not configure kill-on-close Job Object: %w", err)
	}

	var handles [3]windows.Handle
	current := windows.CurrentProcess()
	for i, f := range []*os.File{stdin, stdout, stderr} {
		err := windows.DuplicateHandle(current, windows.Handle(f.Fd()), current, &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS)
		runtime.KeepAlive(f)
		if err != nil {
			return nil, err
		}
		defer windows.CloseHandle(handles[i])
	}

	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return nil, err
	}
	defer attrs.Delete()
	jobs := [1]windows.Handle{job}
	if err := attrs.Update(procThreadAttributeJobList, unsafe.Pointer(&jobs[0]), unsafe.Sizeof(jobs)); err != nil {
		return nil, fmt.Errorf("could not require creation-time Job enrollment: %w", err)
	}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return nil, err
	}
	startup := windows.StartupInfoEx{
		StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES,
			StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2],
		},
		ProcThreadAttributeList: attrs.List(),
	}
	var info windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW |
		windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	err = windows.CreateProcess(program, &commandLine[0], nil, nil, true, flags, &env[0], dir, &startup.StartupInfo, &info)
	if err != nil {
		return nil, fmt.Errorf("could not create core inside its Job Object: %w", err)
	}
	defer windows.CloseHandle(info.Thread)
	if _, err := windows.ResumeThread(info.Thread); err != nil {
		_ = windows.TerminateJobObject(job, 1)
		_, _ = windows.WaitForSingleObject(info.Process, 5000)
		windows.CloseHandle(info.Process)
		return nil, fmt.Errorf("could not resume the contained core: %w", err)
	}
	success = true
	return &platformProcess{job: job, process: info.Process, pid: int(info.ProcessId)}, nil
}

func windowsEnvironment(overrides map[string]string) ([]uint16, error) {
	var block []uint16
	for _, entry := range runtimeEnvironment(overrides, true) {
		wide, err := syscall.UTF16FromString(entry)
		if err != nil {
			return nil, err
		}
		block = append(block, wide...)
	}
	if len(block) == 0 {
		block = append(block, 0)
	}
	return append(block, 0), nil
}

func isPortOccupied(err error) bool { return errors.Is(err, windows.WSAEADDRINUSE) }

func newOutputPipe() (*os.File, *os.File, error) {
	// Only the temporary copies in the explicit handle list are inheritable.
	var reader, writer windows.Handle
	if err := windows.CreatePipe(&reader, &writer, nil, 0); err != nil {
		return nil, nil, err
	}
	return os.NewFile(uintptr(reader), "core output reader"), os.NewFile(uintptr(writer), "core output writer"), nil
}

func (p *platformProcess) id() int { return p.pid }

func (p *platformProcess) leaderExited() (bool, error) {
	result, err := windows.WaitForSingleObject(p.process, 0)
	if err != nil {
		return false, err
	}
	switch result {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, errors.New("unexpected core process wait result")
	}
}

func (p *platformProcess) requestGracefulStop() (bool, error) {
	// CREATE_NO_WINDOW leaves no shared console for CTRL_BREAK.
	return false, nil
}

func (p *platformProcess) killTree() error {
	if p.killSent {
		return nil
	}
	if err := windows.TerminateJobObject(p.job, 1); err != nil {
		if empty, checkErr := p.treeEmpty(); checkErr != nil || !empty {
			return fmt.Errorf("could not terminate the core Job Object: %w", err)
		}
	}
	p.killSent = true
	return nil
}

func (p *platformProcess) treeEmpty() (bool, error) {
	// Layout of JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
	var accounting struct {
		TotalUserTime, TotalKernelTime            int64
		ThisPeriodUserTime, ThisPeriodKernelTime  int64
		TotalPageFaultCount, TotalProcesses       uint32
		ActiveProcesses, TotalTerminatedProcesses uint32
	}
	var pinned runtime.Pinner
	pinned.Pin(&accounting)
	defer pinned.Unpin()
	err := windows.QueryInformationJobObject(p.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
	if err != nil || accounting.ActiveProcesses != 0 {
		return false, err
	}
	return p.leaderExited()
}

func (p *platformProcess) exitDetail() string {
	var code uint32
	if err := windows.GetExitCodeProcess(p.process, &code); err != nil {
		return ""
	}
	return fmt.Sprintf("exit code %d", code)
}

func (p *platformProcess) close() {
	windows.CloseHandle(p.job)
	windows.CloseHandle(p.process)
}

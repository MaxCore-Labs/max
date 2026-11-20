//go:build windows

package engine

import (
	"os/exec"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var jobHandles sync.Map // map[int]windows.Handle

func configureGroup(_ *exec.Cmd) {}

func registerGroup(cmd *exec.Cmd) (int, error) {
	pid := cmd.Process.Pid
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return pid, nil
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	_, _ = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	procHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(pid),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return pid, nil
	}
	assignErr := windows.AssignProcessToJobObject(job, procHandle)
	_ = windows.CloseHandle(procHandle)
	if assignErr != nil {
		_ = windows.CloseHandle(job)
		return pid, nil
	}
	jobHandles.Store(pid, job)
	return pid, nil
}

func cleanupGroup(pgid int) {
	if v, ok := jobHandles.LoadAndDelete(pgid); ok {
		_ = windows.CloseHandle(v.(windows.Handle))
	}
}

// KillGroup terminates the Windows Job Object and process tree rooted at pgid.
func KillGroup(pgid int, _ time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if v, ok := jobHandles.LoadAndDelete(pgid); ok {
		job := v.(windows.Handle)
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	killTreeSnapshot(uint32(pgid))
	return nil
}

func killTreeSnapshot(rootPID uint32) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		terminatePID(rootPID)
		return
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	children := make(map[uint32][]uint32)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err == nil {
		for {
			children[pe.ParentProcessID] = append(children[pe.ParentProcessID], pe.ProcessID)
			pe.Size = uint32(unsafe.Sizeof(pe))
			if err := windows.Process32Next(snap, &pe); err != nil {
				break
			}
		}
	}

	var walk func(uint32)
	walk = func(pid uint32) {
		for _, ch := range children[pid] {
			walk(ch)
		}
		terminatePID(pid)
	}
	walk(rootPID)
}

func terminatePID(pid uint32) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return
	}
	_ = windows.TerminateProcess(h, 1)
	_ = windows.CloseHandle(h)
}

// ProcessAlive reports whether pid is still running on Windows.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	ev, err := windows.WaitForSingleObject(h, 0)
	return err == nil && ev == uint32(windows.WAIT_TIMEOUT)
}

//go:build !windows

package supervisor

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

func configureGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func registerGroup(cmd *exec.Cmd) (int, error) {
	return cmd.Process.Pid, nil
}

func cleanupGroup(_ int) {}

func killGroup(pgid int, grace time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if grace <= 0 {
		grace = 2 * time.Second
	}
	err := syscall.Kill(-pgid, syscall.SIGTERM)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	err = syscall.Kill(-pgid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}

// ProcessAlive reports whether pid exists in the OS process table.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil && pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = cmd.Process.Kill()
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err != nil {
		return false
	}
	// Zombie ('Z') is already terminated, waiting to be reaped or already reaped
	data, rerr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if rerr == nil {
		s := string(data)
		if idx := strings.LastIndexByte(s, ')'); idx != -1 && idx+2 < len(s) {
			state := s[idx+2]
			if state == 'Z' || state == 'X' {
				return false
			}
		}
	}
	return true
}

// measureProcessTreeKB walks /proc to sum VmRSS (in KiB) of rootPID and all descendants.
func measureProcessTreeKB(rootPID int) (int64, error) {
	if rootPID <= 0 {
		return 0, fmt.Errorf("invalid root PID %d", rootPID)
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("read /proc: %w", err)
	}

	type procStat struct {
		ppid  int
		rssKB int64
	}
	procs := make(map[int]procStat, len(entries))
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, perr := strconv.Atoi(ent.Name())
		if perr != nil || pid <= 0 {
			continue
		}
		statusBytes, rerr := os.ReadFile(filepath.Join("/proc", ent.Name(), "status"))
		if rerr != nil {
			continue
		}
		var ppid int
		var rssKB int64
		for _, line := range strings.Split(string(statusBytes), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					ppid, _ = strconv.Atoi(fields[1])
				}
			} else if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					rssKB, _ = strconv.ParseInt(fields[1], 10, 64)
				}
			}
		}
		procs[pid] = procStat{ppid: ppid, rssKB: rssKB}
	}

	rootInfo, ok := procs[rootPID]
	if !ok {
		return 0, fmt.Errorf("root PID %d not found in /proc", rootPID)
	}

	inTree := map[int]bool{rootPID: true}
	changed := true
	for changed {
		changed = false
		for pid, st := range procs {
			if !inTree[pid] && inTree[st.ppid] {
				inTree[pid] = true
				changed = true
			}
		}
	}

	totalKB := rootInfo.rssKB
	for pid := range inTree {
		if pid != rootPID {
			totalKB += procs[pid].rssKB
		}
	}
	return totalKB, nil
}

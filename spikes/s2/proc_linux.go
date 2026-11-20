//go:build linux

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func readStatusKB(path, prefix string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				if kb, err := strconv.ParseFloat(fields[0], 64); err == nil {
					return kb
				}
			}
		}
	}
	return 0
}

// peakSelfRSSMB reads VmHWM from /proc/self/status, capturing off-heap libc
// allocations made by modernc.org/sqlite alongside the Go runtime.
func peakSelfRSSMB() float64 {
	return readStatusKB("/proc/self/status", "VmHWM:") / 1024.0
}

// currentSelfPSSMB reads Pss from /proc/self/smaps_rollup (falls back to VmRSS).
func currentSelfPSSMB() float64 {
	if kb := readStatusKB("/proc/self/smaps_rollup", "Pss:"); kb > 0 {
		return kb / 1024.0
	}
	return readStatusKB("/proc/self/status", "VmRSS:") / 1024.0
}

type procMeta struct {
	pid     int
	ppid    int
	pgid    int
	cmdline string
}

func scanLinuxProcs() []procMeta {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	out := make([]procMeta, 0, 64)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		statBytes, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		// Format: pid (comm) state ppid pgrp ...
		rparen := bytes.LastIndexByte(statBytes, ')')
		if rparen < 0 || rparen+2 >= len(statBytes) {
			continue
		}
		fields := strings.Fields(string(statBytes[rparen+2:]))
		if len(fields) < 3 || fields[0] == "Z" {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		pgid, _ := strconv.Atoi(fields[2])
		cmdRaw, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		envRaw, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		cmdline := strings.ReplaceAll(string(cmdRaw), "\x00", " ") + " " + strings.ReplaceAll(string(envRaw), "\x00", " ")
		out = append(out, procMeta{pid: pid, ppid: ppid, pgid: pgid, cmdline: cmdline})
	}
	return out
}

func matchDaemonPIDs(rootPID int, exactDataDir string) []int {
	selfPID := os.Getpid()
	procs := scanLinuxProcs()
	matched := make(map[int]bool)

	for _, p := range procs {
		if p.pid == selfPID {
			continue
		}
		if (rootPID > 1 && (p.pid == rootPID || p.pgid == rootPID)) ||
			(exactDataDir != "" && strings.Contains(p.cmdline, exactDataDir)) {
			matched[p.pid] = true
		}
	}
	added := true
	for added {
		added = false
		for _, p := range procs {
			if p.pid != selfPID && !matched[p.pid] && matched[p.ppid] {
				matched[p.pid] = true
				added = true
			}
		}
	}
	res := make([]int, 0, len(matched))
	for pid := range matched {
		res = append(res, pid)
	}
	return res
}

func measureDaemonTreeMB(rootPID int, exactDataDir string) (pssMB float64, rssMB float64, count int) {
	pids := matchDaemonPIDs(rootPID, exactDataDir)
	var pssKB, rssKB float64
	for _, pid := range pids {
		pStr := strconv.Itoa(pid)
		rss := readStatusKB(filepath.Join("/proc", pStr, "status"), "VmRSS:")
		pss := readStatusKB(filepath.Join("/proc", pStr, "smaps_rollup"), "Pss:")
		if pss <= 0 {
			pss = rss
		}
		pssKB += pss
		rssKB += rss
	}
	return pssKB / 1024.0, rssKB / 1024.0, len(pids)
}

func killDaemonTree(rootPID int, exactDataDir string) {
	if rootPID > 1 {
		_ = syscall.Kill(-rootPID, syscall.SIGKILL)
	}
	for _, pid := range matchDaemonPIDs(rootPID, exactDataDir) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

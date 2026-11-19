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

func sendBrowserGracefulStop(cmd *exec.Cmd) bool {
	if cmd == nil || cmd.Process == nil {
		return false
	}
	// Match cloakserve on Linux: SIGINT flushes Chrome's SQLite cookie store cleanly.
	return cmd.Process.Signal(syscall.SIGINT) == nil
}

func readProcKB(path, prefix string) float64 {
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

func measureProcessTreeMB(rootPID int) (pssMB float64, rssMB float64, count int) {
	if rootPID <= 1 {
		return 0, 0, 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, 0, 0
	}
	type pInfo struct {
		pid  int
		ppid int
		pgid int
	}
	var all []pInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 1 {
			continue
		}
		sb, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		rp := bytes.LastIndexByte(sb, ')')
		if rp < 0 || rp+2 >= len(sb) {
			continue
		}
		f := strings.Fields(string(sb[rp+2:]))
		if len(f) < 3 {
			continue
		}
		ppid, _ := strconv.Atoi(f[1])
		pgid, _ := strconv.Atoi(f[2])
		all = append(all, pInfo{pid: pid, ppid: ppid, pgid: pgid})
	}

	matched := map[int]bool{rootPID: true}
	for _, p := range all {
		if p.pgid == rootPID {
			matched[p.pid] = true
		}
	}
	added := true
	for added {
		added = false
		for _, p := range all {
			if !matched[p.pid] && matched[p.ppid] {
				matched[p.pid] = true
				added = true
			}
		}
	}

	var pssKB, rssKB float64
	alive := 0
	for pid := range matched {
		pStr := strconv.Itoa(pid)
		rss := readProcKB(filepath.Join("/proc", pStr, "status"), "VmRSS:")
		if rss <= 0 {
			continue
		}
		alive++
		pss := readProcKB(filepath.Join("/proc", pStr, "smaps_rollup"), "Pss:")
		if pss <= 0 {
			pss = rss
		}
		pssKB += pss
		rssKB += rss
	}
	return pssKB / 1024.0, rssKB / 1024.0, alive
}

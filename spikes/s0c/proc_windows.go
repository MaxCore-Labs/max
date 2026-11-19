//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func configureProcessGroup(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}

// measureProcessTreeKB sums WorkingSetSize (in KiB) of rootPID and all descendants on Windows.
func measureProcessTreeKB(rootPID int) (int64, error) {
	if rootPID <= 0 {
		return 0, fmt.Errorf("invalid root PID %d", rootPID)
	}
	psCmd := fmt.Sprintf(`
$all = Get-CimInstance Win32_Process
$root = $all | Where-Object { $_.ProcessId -eq %d }
if (-not $root) { exit 2 }
$ids = [System.Collections.Generic.HashSet[uint32]]::new()
[void]$ids.Add(%d)
$added = $true
while ($added) {
  $added = $false
  foreach ($p in $all) {
    if ($ids.Contains($p.ParentProcessId) -and -not $ids.Contains($p.ProcessId)) {
      [void]$ids.Add($p.ProcessId)
      $added = $true
    }
  }
}
$sum = ($all | Where-Object { $ids.Contains($_.ProcessId) } | Measure-Object -Property WorkingSetSize -Sum).Sum
Write-Output $sum
`, rootPID, rootPID)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", psCmd).Output()
	if err != nil {
		return 0, fmt.Errorf("measure Win32_Process tree for PID %d: %w", rootPID, err)
	}
	val, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse WorkingSetSize %q: %w", string(out), err)
	}
	return val / 1024, nil
}

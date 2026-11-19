//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func setProcessGroup(_ *exec.Cmd) {}

func sendBrowserGracefulStop(_ *exec.Cmd) bool {
	return false
}

func measureProcessTreeMB(rootPID int) (pssMB float64, rssMB float64, count int) {
	if rootPID <= 1 {
		return 0, 0, 0
	}
	ps := fmt.Sprintf(`
$all = Get-CimInstance Win32_Process
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
$matched = $all | Where-Object { $ids.Contains($_.ProcessId) }
$ws = ($matched | Measure-Object -Property WorkingSetSize -Sum).Sum
$priv = ($matched | Measure-Object -Property PrivatePageCount -Sum).Sum
if (-not $ws) { $ws = 0 }
if (-not $priv) { $priv = 0 }
Write-Output "$($matched.Count),$priv,$ws"
`, rootPID)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return 0, 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 3 {
		return 0, 0, 0
	}
	cnt, _ := strconv.Atoi(parts[0])
	priv, _ := strconv.ParseFloat(parts[1], 64)
	ws, _ := strconv.ParseFloat(parts[2], 64)
	return priv / (1024 * 1024), ws / (1024 * 1024), cnt
}

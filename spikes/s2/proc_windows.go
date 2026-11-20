//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

func setProcessGroup(_ *exec.Cmd) {}

type processMemoryCountersEx struct {
	cb                         uint32
	pageFaultCount             uint32
	peakWorkingSetSize         uintptr
	workingSetSize             uintptr
	quotaPeakPagedPoolUsage    uintptr
	quotaPagedPoolUsage        uintptr
	quotaPeakNonPagedPoolUsage uintptr
	quotaNonPagedPoolUsage     uintptr
	pagefileUsage              uintptr
	peakPagefileUsage          uintptr
	privateUsage               uintptr
}

var (
	modPsapi                 = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = modPsapi.NewProc("GetProcessMemoryInfo")
)

func winSelfMemory() (privateMB, peakWSMB float64) {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, 0
	}
	var pmc processMemoryCountersEx
	pmc.cb = uint32(unsafe.Sizeof(pmc))
	r1, _, _ := procGetProcessMemoryInfo.Call(uintptr(h), uintptr(unsafe.Pointer(&pmc)), uintptr(pmc.cb))
	if r1 == 0 {
		return 0, 0
	}
	return float64(pmc.privateUsage) / (1024 * 1024), float64(pmc.peakWorkingSetSize) / (1024 * 1024)
}

func peakSelfRSSMB() float64 {
	_, peak := winSelfMemory()
	return peak
}

func currentSelfPSSMB() float64 {
	cur, _ := winSelfMemory()
	return cur
}

func measureDaemonTreeMB(rootPID int, exactDataDir string) (pssMB float64, rssMB float64, count int) {
	safeDir := strings.ReplaceAll(exactDataDir, "'", "''")
	ps := fmt.Sprintf(`
$selfPid = %d
$rootPid = %d
$dir = '%s'
$all = Get-CimInstance Win32_Process
$ids = [System.Collections.Generic.HashSet[uint32]]::new()
foreach ($p in $all) {
  if ($p.ProcessId -eq $selfPid -or $p.Name -eq 'powershell.exe') { continue }
  if (($rootPid -gt 1 -and $p.ProcessId -eq $rootPid) -or ($dir -ne '' -and $p.CommandLine -and $p.CommandLine.Contains($dir))) {
    [void]$ids.Add($p.ProcessId)
  }
}
$added = $true
while ($added) {
  $added = $false
  foreach ($p in $all) {
    if ($p.ProcessId -ne $selfPid -and $ids.Contains($p.ParentProcessId) -and -not $ids.Contains($p.ProcessId)) {
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
`, os.Getpid(), rootPID, safeDir)
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).Output()
	if err != nil {
		return 0, 0, 0
	}
	parts := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(parts) != 3 {
		return 0, 0, 0
	}
	cnt, _ := strconv.Atoi(parts[0])
	privBytes, _ := strconv.ParseFloat(parts[1], 64)
	wsBytes, _ := strconv.ParseFloat(parts[2], 64)
	return privBytes / (1024 * 1024), wsBytes / (1024 * 1024), cnt
}

func killDaemonTree(rootPID int, exactDataDir string) {
	if rootPID > 1 {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(rootPID)).Run()
	}
	if exactDataDir == "" {
		return
	}
	safeDir := strings.ReplaceAll(exactDataDir, "'", "''")
	ps := fmt.Sprintf(
		"Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne %d -and $_.Name -ne 'powershell.exe' -and $_.CommandLine -and $_.CommandLine.Contains('%s') } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }",
		os.Getpid(), safeDir,
	)
	_ = exec.Command("powershell", "-NoProfile", "-Command", ps).Run()
}

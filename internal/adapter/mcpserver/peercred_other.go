//go:build !linux && !windows

package mcpserver

import (
	"net"
	"os/exec"
	"strconv"
	"strings"
)

func peerPIDFromConn(_ *net.UnixConn) int {
	return 0
}

func snapshotProcessParents() map[int]int {
	return nil
}

func lookupSingleParentPID(pid int) (int, bool) {
	if pid <= 1 {
		return 0, false
	}
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}

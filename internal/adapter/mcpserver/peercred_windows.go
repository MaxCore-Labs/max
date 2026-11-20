//go:build windows

package mcpserver

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

const sioAFUnixGetPeerPID = 0x58000100

func peerPIDFromConn(conn *net.UnixConn) int {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0
	}
	var peerPID uint32
	_ = raw.Control(func(fd uintptr) {
		var bytesRet uint32
		_ = windows.WSAIoctl(
			windows.Handle(fd),
			sioAFUnixGetPeerPID,
			nil,
			0,
			(*byte)(unsafe.Pointer(&peerPID)),
			uint32(unsafe.Sizeof(peerPID)),
			&bytesRet,
			nil,
			0,
		)
	})
	return int(peerPID)
}

func snapshotProcessParents() map[int]int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer func() { _ = windows.CloseHandle(snap) }()

	parents := make(map[int]int, 128)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if err := windows.Process32First(snap, &pe); err == nil {
		for {
			parents[int(pe.ProcessID)] = int(pe.ParentProcessID)
			pe.Size = uint32(unsafe.Sizeof(pe))
			if err := windows.Process32Next(snap, &pe); err != nil {
				break
			}
		}
	}
	return parents
}

func lookupSingleParentPID(_ int) (int, bool) {
	return 0, false
}

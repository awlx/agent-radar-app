//go:build windows

package collector

import (
	"os"
	"syscall"
)

const processQueryLimitedInformation = 0x1000

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == 259 // STILL_ACTIVE
}

// Windows has no inode. Rotation is still found when the file gets smaller.
func fileIno(os.FileInfo) uint64 { return 0 }

package collector

import (
	"os"
	"syscall"
)

// fileBirth gives the creation time of a file in milliseconds, or the modification time.
func fileBirth(fi os.FileInfo) int64 {
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok && (d.CreationTime.HighDateTime != 0 || d.CreationTime.LowDateTime != 0) {
		return d.CreationTime.Nanoseconds() / 1e6
	}
	return fi.ModTime().UnixMilli()
}

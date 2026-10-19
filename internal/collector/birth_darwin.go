package collector

import (
	"os"
	"syscall"
)

// fileBirth gives the creation time of a file in milliseconds, or the modification time.
func fileBirth(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && (st.Birthtimespec.Sec != 0 || st.Birthtimespec.Nsec != 0) {
		return st.Birthtimespec.Sec*1000 + st.Birthtimespec.Nsec/1e6
	}
	return fi.ModTime().UnixMilli()
}

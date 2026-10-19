//go:build !darwin && !windows

package collector

import "os"

// fileBirth gives the modification time of a file in milliseconds. This platform does not give
// the creation time in os.FileInfo.
func fileBirth(fi os.FileInfo) int64 {
	return fi.ModTime().UnixMilli()
}

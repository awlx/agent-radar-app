package collector

import (
	"syscall"
	"time"
)

// setBirth sets the creation time of a file. os.Chtimes does not change it on Windows.
func setBirth(path string, at time.Time) error {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := syscall.CreateFile(p, syscall.FILE_WRITE_ATTRIBUTES, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	defer syscall.CloseHandle(h)
	ft := syscall.NsecToFiletime(at.UnixNano())
	return syscall.SetFileTime(h, &ft, nil, nil)
}

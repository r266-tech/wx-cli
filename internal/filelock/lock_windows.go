//go:build windows

package filelock

import (
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32   = syscall.NewLazyDLL("kernel32.dll")
	lockFile   = kernel32.NewProc("LockFileEx")
	unlockFile = kernel32.NewProc("UnlockFileEx")
)

func lock(f *os.File) (func(), error) {
	var overlapped syscall.Overlapped
	ok, _, err := lockFile.Call(f.Fd(), 2, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		if err == syscall.Errno(0) {
			err = syscall.EINVAL
		}
		return nil, err
	}
	return func() { _, _, _ = unlockFile.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped))) }, nil
}

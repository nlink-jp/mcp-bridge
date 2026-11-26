//go:build windows

package oauth

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// kernel32 is a KnownDLL, so it always loads from the system directory.
var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

const (
	lockfileFailImmediately = 0x1
	lockfileExclusiveLock   = 0x2

	errorLockViolation syscall.Errno = 33 // ERROR_LOCK_VIOLATION
)

// tryLockFile takes an exclusive lock on the first byte of the file without
// blocking. It reports false with no error when another handle holds it.
// Locks belong to the handle, so two opens in one process exclude each other.
func tryLockFile(f *os.File) (bool, error) {
	ol := new(syscall.Overlapped)
	r, _, err := procLockFileEx.Call(f.Fd(), lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0, uintptr(unsafe.Pointer(ol)))
	if r != 0 {
		return true, nil
	}
	if errors.Is(err, errorLockViolation) {
		return false, nil
	}
	return false, err
}

func unlockFile(f *os.File) {
	ol := new(syscall.Overlapped)
	_, _, _ = procUnlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(ol)))
}

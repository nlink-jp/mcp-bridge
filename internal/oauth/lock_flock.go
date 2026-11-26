//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package oauth

import (
	"errors"
	"os"
	"syscall"
)

// tryLockFile takes an exclusive flock without blocking. It reports false
// with no error when another open file holds the lock. flock locks belong to
// the open file, so two opens in one process exclude each other as well.
func tryLockFile(f *os.File) (bool, error) {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.EWOULDBLOCK), errors.Is(err, syscall.EAGAIN):
			return false, nil
		default:
			return false, err
		}
	}
}

func unlockFile(f *os.File) {
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

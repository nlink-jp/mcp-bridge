//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package oauth

import (
	"errors"
	"os"
)

// tryLockFile reports that this OS offers no file lock through the standard
// library. Callers treat that like a read-only state directory: they warn and
// carry on uncoordinated (ADR-0007, decision 4).
func tryLockFile(*os.File) (bool, error) {
	return false, errors.New("file locking is not supported on this OS")
}

func unlockFile(*os.File) {}

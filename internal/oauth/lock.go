package oauth

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// lockWait bounds how long a process waits for another to finish with the
// token file. It is longer than a renewal request may take (30 s, see
// defaultHTTPClient), and short enough that an MCP client still gets an
// answer when the holder is stuck (ADR-0007, decision 4).
const lockWait = 45 * time.Second

// lockPoll is how often a busy lock is tried again.
const lockPoll = 50 * time.Millisecond

var (
	// errLockUnavailable means no process can coordinate through this lock:
	// the lock file cannot be opened (a read-only state directory) or the OS
	// has no file locking. Callers carry on without it.
	errLockUnavailable = errors.New("the token file cannot be locked")

	// errLockTimeout means another process held the lock past the limit.
	// Callers must not present a refresh token after this: renewing without
	// the lock is exactly the race the lock exists to prevent.
	errLockTimeout = errors.New("another mcp-bridge process kept the token file locked")
)

// lockTokens takes an exclusive lock beside the token file, so that only one
// mcp-bridge process at a time reads, decides on and rewrites it (ADR-0007).
//
// The lock is on a separate file because SaveTokens replaces tokens.json by
// renaming over it, which would leave a lock on tokens.json behind on the old
// file. The lock file is never deleted: deleting a file another process may
// hold lets two processes lock two different files. The kernel releases the
// lock when a process dies, so there is no stale lock.
func lockTokens(tokensPath string, wait time.Duration) (unlock func(), err error) {
	f, err := os.OpenFile(tokensPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errLockUnavailable, err)
	}
	deadline := time.Now().Add(wait)
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%w: %v", errLockUnavailable, err)
		}
		if locked {
			return func() {
				unlockFile(f)
				f.Close()
			}, nil
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("%w for %s", errLockTimeout, wait)
		}
		time.Sleep(lockPoll)
	}
}

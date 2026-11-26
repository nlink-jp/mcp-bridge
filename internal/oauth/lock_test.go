package oauth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLockExcludesASecondHolderUntilReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")

	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	// A second open of the lock file, even in the same process, is excluded:
	// this is what lets the tests stand several Providers in for processes.
	start := time.Now()
	if _, err := lockTokens(path, 150*time.Millisecond); !errors.Is(err, errLockTimeout) {
		t.Fatalf("second lock while held: err = %v, want errLockTimeout", err)
	}
	if waited := time.Since(start); waited < 150*time.Millisecond {
		t.Errorf("gave up after %s, before the limit", waited)
	}

	unlock()
	unlock2, err := lockTokens(path, 150*time.Millisecond)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	unlock2()
}

func TestLockWaitsForTheHolderToFinish(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		unlock()
	}()
	unlock2, err := lockTokens(path, 5*time.Second)
	if err != nil {
		t.Fatalf("waiting for a released lock: %v", err)
	}
	unlock2()
}

func TestLockFileIsPrivateAndKept(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	unlock()

	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("the lock file must stay after release: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("lock file mode = %o, want 600", mode)
	}
}

func TestLockIsUnavailableWhereTheFileCannotBeOpened(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "tokens.json")
	if _, err := lockTokens(path, time.Second); !errors.Is(err, errLockUnavailable) {
		t.Fatalf("err = %v, want errLockUnavailable", err)
	}
}

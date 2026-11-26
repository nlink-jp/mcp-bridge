package oauth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// releaseAfter holds the token lock for path and releases it after d.
func releaseAfter(t *testing.T, path string, d time.Duration) {
	t.Helper()
	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(d)
		unlock()
	}()
}

// A login's tokens are saved under the lock, so a renewal of the old login
// in progress finishes first and cannot overwrite the new one afterwards.
func TestLoginSaveWaitsForARenewalInProgress(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "old", RefreshToken: "old-rt"})
	releaseAfter(t, path, 150*time.Millisecond)

	var out bytes.Buffer
	start := time.Now()
	err := saveLogin(LoginConfig{ServerName: "s", TokensPath: path, Out: &out, lockWait: 5 * time.Second},
		&Tokens{AccessToken: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Error("the login was saved while another process held the lock")
	}
	if got, _ := LoadTokens(path); got.AccessToken != "new" {
		t.Errorf("stored %q, want the new login", got.AccessToken)
	}
	if out.Len() != 0 {
		t.Errorf("an ordinary save printed %q", out.String())
	}
}

// A login the user has just completed is never thrown away for want of the
// lock.
func TestLoginSaveGoesAheadPastTheLimit(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "old"})
	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	var out bytes.Buffer
	err = saveLogin(LoginConfig{ServerName: "s", TokensPath: path, Out: &out, lockWait: 100 * time.Millisecond},
		&Tokens{AccessToken: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadTokens(path); got.AccessToken != "new" {
		t.Errorf("stored %q, want the new login", got.AccessToken)
	}
	if !strings.Contains(out.String(), "saved without waiting") {
		t.Errorf("the user was not told: %q", out.String())
	}
}

func TestDeleteLoginWaitsForARenewalInProgress(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "x"})
	releaseAfter(t, path, 150*time.Millisecond)

	start := time.Now()
	warning, err := deleteLogin(path, 5*time.Second)
	if err != nil || warning != "" {
		t.Fatalf("deleteLogin: %q, %v", warning, err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Error("the login was deleted while another process held the lock")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the token file survived")
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Errorf("the lock file must stay: %v", err)
	}
}

func TestDeleteLoginDeletesNothingPastTheLimit(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "x"})
	unlock, err := lockTokens(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	if _, err := deleteLogin(path, 100*time.Millisecond); !errors.Is(err, errLockTimeout) {
		t.Fatalf("err = %v, want errLockTimeout", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the login was deleted without the lock: %v", err)
	}
}

// A server never logged in to has no state directory; logging out of it is
// the existing "nothing to do", with no warning about a lock.
func TestDeleteLoginWithoutAStateDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-logged-in", "tokens.json")
	warning, err := DeleteLogin(path)
	if err != nil || warning != "" {
		t.Errorf("DeleteLogin: %q, %v", warning, err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Error("logging out created a state directory")
	}
}

func TestDeleteLoginWhereTheLockCannotBeUsed(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "x"})
	// A read-only directory would also stop the deletion, so the lock is made
	// unusable another way: a directory where the lock file should be.
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	warning, err := deleteLogin(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warning, "cannot be locked") {
		t.Errorf("warning = %q", warning)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the token file survived")
	}
}

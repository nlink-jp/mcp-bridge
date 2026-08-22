package tokencmd

import (
	"strings"
	"testing"
	"time"
)

func TestTokenFromCommandOutput(t *testing.T) {
	p := New("printf", []string{"ya29.the-token"}, time.Minute)
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "ya29.the-token" {
		t.Errorf("token = %q", got)
	}
}

// A trailing newline is what every well-behaved command prints.
func TestTrailingNewlineIsTrimmed(t *testing.T) {
	p := New("sh", []string{"-c", "echo the-token"}, time.Minute)
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "the-token" {
		t.Errorf("token = %q, want the newline trimmed", got)
	}
}

func TestTokenIsCachedWithinTTL(t *testing.T) {
	// Each run of this command produces a different value, so a second
	// distinct value proves the cache was bypassed.
	p := New("sh", []string{"-c", "date +%s%N"}, time.Minute)
	first, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("the command ran twice inside its TTL")
	}

	p.Invalidate()
	third, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Error("Invalidate did not force the command to run again")
	}
}

func TestExpiredCacheRefetches(t *testing.T) {
	p := New("sh", []string{"-c", "date +%s%N"}, time.Nanosecond)
	first, _ := p.Token()
	time.Sleep(time.Millisecond)
	second, _ := p.Token()
	if first == second {
		t.Error("an expired cache entry was reused")
	}
}

// The stderr of a failing command is the whole diagnosis; dropping it leaves
// the user with an exit status.
func TestFailureCarriesStderr(t *testing.T) {
	p := New("sh", []string{"-c", "echo 'not logged in' >&2; exit 1"}, time.Minute)
	_, err := p.Token()
	if err == nil {
		t.Fatal("a failing command was accepted")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("stderr was dropped from the error: %v", err)
	}
}

func TestEmptyOutputIsAnError(t *testing.T) {
	p := New("true", nil, time.Minute)
	if _, err := p.Token(); err == nil {
		t.Error("a command producing no output was accepted")
	}
}

// A command printing several lines has printed something other than a token.
// Silently taking the first line would send a broken credential upstream.
func TestMultiLineOutputIsAnError(t *testing.T) {
	p := New("sh", []string{"-c", "echo line1; echo line2"}, time.Minute)
	_, err := p.Token()
	if err == nil {
		t.Fatal("multi-line output was accepted as a token")
	}
	if !strings.Contains(err.Error(), "multi-line") {
		t.Errorf("unclear error: %v", err)
	}
}

func TestMissingCommandIsReported(t *testing.T) {
	p := New("mcp-bridge-no-such-command", nil, time.Minute)
	_, err := p.Token()
	if err == nil {
		t.Fatal("a missing command was accepted")
	}
	if !strings.Contains(err.Error(), "mcp-bridge-no-such-command") {
		t.Errorf("the error does not name the command: %v", err)
	}
}

// The command must not run through a shell: a config value containing shell
// metacharacters would otherwise become a second command.
func TestArgumentsAreNotShellInterpreted(t *testing.T) {
	p := New("printf", []string{"%s", "tok; touch /tmp/mcp-bridge-should-not-exist"}, time.Minute)
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, ";") {
		t.Errorf("the argument was interpreted rather than passed through: %q", got)
	}
}

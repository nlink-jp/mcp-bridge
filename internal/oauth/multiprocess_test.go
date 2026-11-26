package oauth

// Tests for ADR-0007: several mcp-bridge processes for one server share the
// token file, and against a server that issues single-use refresh tokens
// they must not lose the stored login. Several Providers in one test stand in
// for processes: each opens the lock file separately, and flock / LockFileEx
// exclude separate opens even within one process. TestRealProcessesRenewOnce
// backs that up with real processes.

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rotatingServer is a token endpoint that issues single-use refresh tokens,
// as OAuth 2.1 and RFC 9700 recommend for public clients. With reuse
// detection, a spent refresh token presented again revokes the whole grant.
type rotatingServer struct {
	*httptest.Server

	mu             sync.Mutex
	valid          map[string]bool
	received       []string
	issued         int
	reuseDetection bool

	// holdFirst, when set, runs before the first renewal is answered, so
	// that the other callers are already waiting by the time it completes.
	holdFirst func()
}

func newRotatingServer(t *testing.T, reuse bool, valid ...string) *rotatingServer {
	t.Helper()
	rs := &rotatingServer{valid: map[string]bool{}, reuseDetection: reuse}
	for _, v := range valid {
		rs.valid[v] = true
	}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		rt := r.PostForm.Get("refresh_token")

		rs.mu.Lock()
		rs.received = append(rs.received, rt)
		first := len(rs.received) == 1
		hold := rs.holdFirst
		rs.mu.Unlock()
		if first && hold != nil {
			hold()
		}

		rs.mu.Lock()
		defer rs.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !rs.valid[rt] {
			if rs.reuseDetection {
				rs.valid = map[string]bool{}
			}
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Invalid refresh token"}`)
			return
		}
		delete(rs.valid, rt)
		rs.issued++
		next := fmt.Sprintf("rt-%d", rs.issued)
		rs.valid[next] = true
		fmt.Fprintf(w, `{"access_token":"at-%d","refresh_token":%q,"expires_in":3600,"token_type":"Bearer"}`, rs.issued, next)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *rotatingServer) requests() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.received...)
}

func (rs *rotatingServer) allow(rt string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.valid[rt] = true
}

// noRepeats fails when a refresh token reached the server twice.
func noRepeats(t *testing.T, rs *rotatingServer) {
	t.Helper()
	seen := map[string]bool{}
	for _, rt := range rs.requests() {
		if seen[rt] {
			t.Errorf("refresh token %q was presented twice: %v", rt, rs.requests())
		}
		seen[rt] = true
	}
}

// clock is a settable time shared by the Providers of one test.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Unix(1_800_000_000, 0)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// share is one stored login and the server it belongs to.
type share struct {
	t     *testing.T
	path  string
	rs    *rotatingServer
	clock *clock
}

// newShare stores at-0 / rt-0, valid for an hour, beside a rotating server.
func newShare(t *testing.T, reuse bool) *share {
	t.Helper()
	c := newClock()
	path := storedAt(t, &Tokens{AccessToken: "at-0", RefreshToken: "rt-0", ExpiresAt: c.Now().Add(time.Hour).Unix()})
	return &share{t: t, path: path, rs: newRotatingServer(t, reuse, "rt-0"), clock: c}
}

func (s *share) provider(logs *bytes.Buffer) *Provider {
	s.t.Helper()
	cfg := ProviderConfig{
		ServerName: "atlassian", TokensPath: s.path, TokenURL: s.rs.URL,
		ClientID: "c", ClientAuthMethod: "none", Now: s.clock.Now,
		lockWait: 5 * time.Second,
	}
	if logs != nil {
		cfg.Logs = logs
	}
	p, err := NewProvider(cfg)
	if err != nil {
		s.t.Fatal(err)
	}
	return p
}

// The proposal's test 1, reversed: the second process adopts the first one's
// renewal instead of presenting the refresh token it read at start-up.
func TestSecondProcessAdoptsTheRenewal(t *testing.T) {
	s := newShare(t, false)
	a, b := s.provider(nil), s.provider(nil)
	s.clock.advance(2 * time.Hour)

	ta, err := a.Token()
	if err != nil {
		t.Fatal(err)
	}
	tb, err := b.Token()
	if err != nil {
		t.Fatalf("process B: %v", err)
	}
	if tb != ta {
		t.Errorf("B returned %q, want A's renewal %q", tb, ta)
	}
	if got := s.rs.requests(); len(got) != 1 {
		t.Errorf("renewal requests = %v, want one", got)
	}
}

// The proposal's test 2, reversed: with reuse detection, the stored login
// survives, and a process started afterwards works.
func TestReuseDetectionKeepsTheStoredLogin(t *testing.T) {
	s := newShare(t, true)
	a, b := s.provider(nil), s.provider(nil)
	s.clock.advance(2 * time.Hour)
	if _, err := a.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Token(); err != nil {
		t.Fatalf("process B: %v", err)
	}

	s.clock.advance(2 * time.Hour)
	c := s.provider(nil)
	if _, err := c.Token(); err != nil {
		t.Fatalf("a process started later: %v", err)
	}
	noRepeats(t, s.rs)
}

func TestSixProcessesRenewOnce(t *testing.T) {
	s := newShare(t, true)
	var ps []*Provider
	for i := 0; i < 6; i++ {
		ps = append(ps, s.provider(nil))
	}

	// The first renewal is answered only after every other caller has gone
	// into Token() and had time to reach the lock. Without the lock they
	// would read rt-0 from the file meanwhile and present it.
	var entered atomic.Int32
	s.rs.holdFirst = func() {
		for entered.Load() < int32(len(ps)) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
	}

	run := func() {
		t.Helper()
		var wg sync.WaitGroup
		errs := make([]error, len(ps))
		for i, p := range ps {
			wg.Add(1)
			go func() {
				defer wg.Done()
				entered.Add(1)
				_, errs[i] = p.Token()
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("process %d: %v", i, err)
			}
		}
	}

	s.clock.advance(2 * time.Hour)
	run()
	if got := s.rs.requests(); len(got) != 1 {
		t.Errorf("renewal requests after the first expiry = %v, want one", got)
	}

	s.clock.advance(2 * time.Hour)
	run()
	if got := s.rs.requests(); len(got) != 2 {
		t.Errorf("renewal requests after the second expiry = %v, want two in all", got)
	}
	noRepeats(t, s.rs)
}

// A token the server refused is not adopted from the file, and another
// process that is refused the same token adopts the renewal.
func TestRejectedTokenOnDiskIsNotAdopted(t *testing.T) {
	s := newShare(t, true)
	a, b := s.provider(nil), s.provider(nil)

	a.Invalidate() // the server answered 401 to at-0 before its expiry
	ta, err := a.Token()
	if err != nil {
		t.Fatal(err)
	}
	if ta == "at-0" {
		t.Fatal("A took back the access token the server had just rejected")
	}

	b.Invalidate()
	tb, err := b.Token()
	if err != nil || tb != ta {
		t.Errorf("B: %q, %v; want A's renewal %q", tb, err, ta)
	}
	if got := s.rs.requests(); len(got) != 1 {
		t.Errorf("renewal requests = %v, want one", got)
	}
}

// After a 401, a renewal that fails must not leave the rejected token to be
// handed out by the next call.
func TestFailedRenewalAfterARejectionDoesNotReuseTheToken(t *testing.T) {
	c := newClock()
	path := storedAt(t, &Tokens{AccessToken: "at-0", RefreshToken: "spent", ExpiresAt: c.Now().Add(time.Hour).Unix()})
	rs := newRotatingServer(t, false) // "spent" is not valid
	p, err := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: rs.URL, Now: c.Now, lockWait: time.Second})
	if err != nil {
		t.Fatal(err)
	}

	p.Invalidate()
	if _, err := p.Token(); err == nil {
		t.Fatal("the renewal with a spent refresh token succeeded")
	}
	got, err := p.Token()
	if err == nil {
		t.Fatalf("after the failed renewal Token returned %q", got)
	}
	if len(rs.requests()) != 2 {
		t.Errorf("the second call did not go back to the file and the server: %v", rs.requests())
	}
}

// readOnly makes dir read-only for the rest of the test, or skips where that
// does not stop writes (root, or Windows).
func readOnly(t *testing.T, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions")
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("cannot make the directory read-only")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		f.Close()
		_ = os.Chmod(dir, 0o700)
		_ = os.Remove(f.Name())
		t.Skip("a read-only directory is still writable here (running as root?)")
	}
}

// A renewal that could not be saved leaves the newer refresh token in memory
// only. Renewing again from the file would present a spent token even with
// a single process running.
func TestRenewalThatCouldNotBeSavedKeepsRenewing(t *testing.T) {
	s := newShare(t, true)
	readOnly(t, filepath.Dir(s.path))
	var logs bytes.Buffer
	p := s.provider(&logs)

	for i := 0; i < 2; i++ {
		s.clock.advance(2 * time.Hour)
		if _, err := p.Token(); err != nil {
			t.Fatalf("renewal %d: %v", i+1, err)
		}
	}
	noRepeats(t, s.rs)
	for _, want := range []string{"cannot lock the token file", "could not be saved"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs lack %q:\n%s", want, logs.String())
		}
	}
}

// Once the file has been rewritten since the failed save (here by a new
// login), the file is newer than memory again.
func TestNewLoginAfterAnUnsavedRenewalWins(t *testing.T) {
	s := newShare(t, false)
	dir := filepath.Dir(s.path)
	readOnly(t, dir)
	p := s.provider(nil)
	s.clock.advance(2 * time.Hour)
	if _, err := p.Token(); err != nil {
		t.Fatal(err)
	}

	_ = os.Chmod(dir, 0o700)
	s.rs.allow("login-rt")
	s.clock.advance(2 * time.Hour) // the renewal held in memory expires too
	if err := SaveTokens(s.path, &Tokens{AccessToken: "login-at", RefreshToken: "login-rt", ExpiresAt: s.clock.Now().Add(-time.Minute).Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Token(); err != nil {
		t.Fatal(err)
	}
	reqs := s.rs.requests()
	if last := reqs[len(reqs)-1]; last != "login-rt" {
		t.Errorf("renewed with %q, want the new login's refresh token: %v", last, reqs)
	}
}

// A bridge that failed with invalid_grant picks up a new login at its next
// call, without restarting the MCP client. Before ADR-0007 it kept the dead
// refresh token in memory and failed until restarted.
func TestLoggingInAgainHealsARunningBridge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		login Tokens
	}{
		{"with a refresh token", Tokens{AccessToken: "login-at", RefreshToken: "login-rt", ExpiresAt: 1_800_000_000 + 5*3600}},
		{"without a refresh token", Tokens{AccessToken: "login-at"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newClock()
			path := storedAt(t, &Tokens{AccessToken: "at-0", RefreshToken: "rt-0", ExpiresAt: c.Now().Add(-time.Hour).Unix()})
			rs := newRotatingServer(t, true) // the grant behind rt-0 is dead
			p, err := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: rs.URL, Now: c.Now, lockWait: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Token(); err == nil {
				t.Fatal("renewed against a dead grant")
			}

			rs.allow("login-rt")
			login := tc.login
			if err := SaveTokens(path, &login); err != nil {
				t.Fatal(err)
			}
			got, err := p.Token()
			if err != nil {
				t.Fatalf("after logging in again: %v", err)
			}
			if got != "login-at" {
				t.Errorf("token = %q, want the new login's", got)
			}
		})
	}
}

func TestLogoutIsNotWrittenBack(t *testing.T) {
	s := newShare(t, false)
	p := s.provider(nil)
	if err := DeleteTokens(s.path); err != nil {
		t.Fatal(err)
	}
	s.clock.advance(2 * time.Hour)

	_, err := p.Token()
	if err == nil || !strings.Contains(err.Error(), "mcp-bridge login atlassian") {
		t.Fatalf("err = %v, want one naming the login", err)
	}
	if _, statErr := os.Stat(s.path); !os.IsNotExist(statErr) {
		t.Error("the logged-out login was written back")
	}
	if len(s.rs.requests()) != 0 {
		t.Errorf("a renewal was sent after the logout: %v", s.rs.requests())
	}
}

func TestCorruptFileNamesTheLogin(t *testing.T) {
	s := newShare(t, false)
	p := s.provider(nil)
	if err := os.WriteFile(s.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.clock.advance(2 * time.Hour)
	if _, err := p.Token(); err == nil || !strings.Contains(err.Error(), "mcp-bridge login atlassian") {
		t.Fatalf("err = %v, want one naming the login", err)
	}
}

// With the lock held past the limit, the request fails and no refresh token
// is presented: renewing anyway is the race the lock exists to prevent.
func TestLockHeldPastTheLimitFailsWithoutRenewing(t *testing.T) {
	s := newShare(t, true)
	p, err := NewProvider(ProviderConfig{
		ServerName: "atlassian", TokensPath: s.path, TokenURL: s.rs.URL,
		Now: s.clock.Now, lockWait: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockTokens(s.path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	s.clock.advance(2 * time.Hour)
	_, err = p.Token()
	if err == nil || !strings.Contains(err.Error(), "another mcp-bridge process is renewing the token") {
		t.Fatalf("err = %v", err)
	}
	if len(s.rs.requests()) != 0 {
		t.Errorf("a refresh token was presented without the lock: %v", s.rs.requests())
	}
}

func TestFirstReadWaitsForTheLock(t *testing.T) {
	s := newShare(t, false)
	unlock, err := lockTokens(s.path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ProviderConfig{ServerName: "atlassian", TokensPath: s.path, lockWait: 100 * time.Millisecond}
	if _, err := NewProvider(cfg); err == nil || !strings.Contains(err.Error(), "renewing the token") {
		t.Fatalf("NewProvider with the lock held: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		unlock()
	}()
	cfg.lockWait = 5 * time.Second
	if _, err := NewProvider(cfg); err != nil {
		t.Fatalf("NewProvider after the release: %v", err)
	}
}

// The records say what happened and never carry a token value.
func TestRenewalRecordsSayWhatHappened(t *testing.T) {
	s := newShare(t, false)
	var la, lb bytes.Buffer
	a, b := s.provider(&la), s.provider(&lb)
	s.clock.advance(2 * time.Hour)
	if _, err := a.Token(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Token(); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(la.String(), `renewed the token for "atlassian"`) ||
		!strings.Contains(la.String(), "the server issued a new refresh token") {
		t.Errorf("A's record: %q", la.String())
	}
	if !strings.Contains(lb.String(), `using the token another mcp-bridge process stored for "atlassian"`) {
		t.Errorf("B's record: %q", lb.String())
	}
	for _, logs := range []string{la.String(), lb.String()} {
		for _, secret := range []string{"at-0", "at-1", "rt-0", "rt-1"} {
			if strings.Contains(logs, secret) {
				t.Errorf("a record carries the token value %q: %q", secret, logs)
			}
		}
	}

	// A server that does not rotate says "kept".
	ts := newTokenServer(t, `{"access_token":"fresh","expires_in":3600}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1})
	var lk bytes.Buffer
	p, _ := NewProvider(ProviderConfig{ServerName: "m365", TokensPath: path, TokenURL: ts.URL, Logs: &lk})
	if _, err := p.Token(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(lk.String(), "the refresh token was kept") {
		t.Errorf("record for a server that does not rotate: %q", lk.String())
	}
}

// ---- real processes ----------------------------------------------------

const childEnv = "MCP_BRIDGE_TEST_RENEW_CHILD"

// TestMain lets the test binary act as one `mcp-bridge run` process for
// TestRealProcessesRenewOnce.
func TestMain(m *testing.M) {
	if os.Getenv(childEnv) == "1" {
		os.Exit(renewChild())
	}
	os.Exit(m.Run())
}

// renewChild waits for the start file, renews once and prints the outcome.
func renewChild() int {
	start := os.Getenv("CHILD_START")
	for {
		if _, err := os.Stat(start); err == nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	p, err := NewProvider(ProviderConfig{
		ServerName: "atlassian", TokensPath: os.Getenv("CHILD_TOKENS"),
		TokenURL: os.Getenv("CHILD_TOKEN_URL"), ClientAuthMethod: "none",
	})
	if err == nil {
		var tok string
		tok, err = p.Token()
		if err == nil {
			fmt.Println("ok", tok)
			return 0
		}
	}
	fmt.Println("error", err)
	return 0
}

func TestRealProcessesRenewOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("starts child processes")
	}
	// The stored token has expired by the wall clock the children use.
	path := storedAt(t, &Tokens{AccessToken: "at-0", RefreshToken: "rt-0", ExpiresAt: time.Now().Add(-time.Hour).Unix()})
	rs := newRotatingServer(t, true, "rt-0")
	rs.holdFirst = func() { time.Sleep(500 * time.Millisecond) }
	startFile := filepath.Join(t.TempDir(), "start")

	const n = 6
	outs := make([]*bytes.Buffer, n)
	var cmds []*exec.Cmd
	for i := 0; i < n; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), childEnv+"=1",
			"CHILD_START="+startFile, "CHILD_TOKENS="+path, "CHILD_TOKEN_URL="+rs.URL)
		outs[i] = &bytes.Buffer{}
		cmd.Stdout = outs[i]
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds = append(cmds, cmd)
	}
	if err := os.WriteFile(startFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Errorf("child %d: %v", i, err)
		}
		line, _ := bufio.NewReader(outs[i]).ReadString('\n')
		if !strings.HasPrefix(line, "ok ") {
			t.Errorf("child %d: %s", i, strings.TrimSpace(line))
		}
	}
	if got := rs.requests(); len(got) != 1 {
		t.Errorf("renewal requests from %d processes = %v, want one", n, got)
	}
}

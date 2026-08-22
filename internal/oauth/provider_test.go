package oauth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tokenServer records the form and headers of each token-endpoint call and
// replies with the given body.
type tokenServer struct {
	*httptest.Server
	mu      sync.Mutex
	forms   []url.Values
	headers []http.Header
	status  int
	body    string
}

func newTokenServer(t *testing.T, body string) *tokenServer {
	t.Helper()
	ts := &tokenServer{status: http.StatusOK, body: body}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.forms = append(ts.forms, r.PostForm)
		ts.headers = append(ts.headers, r.Header.Clone())
		status, body := ts.status, ts.body
		ts.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) lastForm(t *testing.T) url.Values {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.forms) == 0 {
		t.Fatal("the token endpoint was never called")
	}
	return ts.forms[len(ts.forms)-1]
}

func (ts *tokenServer) calls() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

func storedAt(t *testing.T, tokens *Tokens) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := SaveTokens(path, tokens); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestProviderRequiresALogin(t *testing.T) {
	_, err := NewProvider(ProviderConfig{ServerName: "slack", TokensPath: filepath.Join(t.TempDir(), "tokens.json")})
	if err == nil {
		t.Fatal("a provider was created with no stored login")
	}
	// The message must carry the command that fixes it.
	if !strings.Contains(err.Error(), `mcp-bridge login slack`) {
		t.Errorf("error does not name the fix: %v", err)
	}
}

func TestProviderRejectsAnEmptyStoredToken(t *testing.T) {
	path := storedAt(t, &Tokens{})
	if _, err := NewProvider(ProviderConfig{ServerName: "slack", TokensPath: path}); err == nil {
		t.Error("an empty stored token was accepted")
	}
}

// ADR-0003. A non-refreshable token is returned as stored, whatever its
// recorded expiry says, because nothing here could act on that expiry — the
// server is the judge, via a 401.
func TestNonRefreshableTokenIsUsedAsStored(t *testing.T) {
	ts := newTokenServer(t, `{}`)
	path := storedAt(t, &Tokens{AccessToken: "slack-token", ExpiresAt: 0})

	p, err := NewProvider(ProviderConfig{ServerName: "slack", TokensPath: path, TokenURL: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "slack-token" {
		t.Errorf("token = %q", got)
	}
	if ts.calls() != 0 {
		t.Error("a non-refreshable token triggered a token-endpoint call")
	}
}

func TestExpiredNonRefreshableTokenIsStillReturned(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "old", ExpiresAt: time.Now().Add(-time.Hour).Unix()})
	p, err := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path})
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Token()
	if err != nil {
		t.Fatalf("an expired but non-refreshable token was refused locally: %v", err)
	}
	if got != "old" {
		t.Errorf("token = %q", got)
	}
}

func TestValidTokenIsNotRefreshed(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"new"}`)
	path := storedAt(t, &Tokens{AccessToken: "current", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "current" {
		t.Errorf("token = %q, want the cached one", got)
	}
	if ts.calls() != 0 {
		t.Error("a valid token was refreshed anyway")
	}
}

func TestExpiredTokenIsRefreshedAndPersisted(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"fresh","expires_in":3600}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r0", ExpiresAt: time.Now().Add(-time.Minute).Unix()})

	p, _ := NewProvider(ProviderConfig{
		ServerName: "s", TokensPath: path, TokenURL: ts.URL,
		ClientID: "id", ClientSecret: "secret",
	})
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "fresh" {
		t.Errorf("token = %q", got)
	}

	form := ts.lastForm(t)
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "r0" {
		t.Errorf("refresh form = %v", form)
	}

	// The refreshed token must survive to the next session.
	reloaded, err := LoadTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.AccessToken != "fresh" {
		t.Errorf("stored token = %q, want the refreshed one", reloaded.AccessToken)
	}
	// A provider that does not rotate refresh tokens omits the field; keeping
	// the old one is what lets the next refresh work.
	if reloaded.RefreshToken != "r0" {
		t.Errorf("refresh token = %q, want the original to be preserved", reloaded.RefreshToken)
	}
}

func TestInvalidateForcesARefresh(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"second","expires_in":3600}`)
	path := storedAt(t, &Tokens{AccessToken: "first", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()})

	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})
	if got, _ := p.Token(); got != "first" {
		t.Fatalf("token = %q", got)
	}
	p.Invalidate()
	got, err := p.Token()
	if err != nil {
		t.Fatal(err)
	}
	if got != "second" {
		t.Errorf("after Invalidate the token = %q, want a refreshed one", got)
	}
}

// A failed refresh must point at the login command: the token is gone and no
// amount of retrying brings it back.
func TestRefreshFailureNamesTheFix(t *testing.T) {
	ts := newTokenServer(t, `{"error":"invalid_grant","error_description":"refresh token revoked"}`)
	ts.mu.Lock()
	ts.status = http.StatusBadRequest
	ts.mu.Unlock()

	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	p, _ := NewProvider(ProviderConfig{ServerName: "slack", TokensPath: path, TokenURL: ts.URL})

	_, err := p.Token()
	if err == nil {
		t.Fatal("a revoked refresh token was accepted")
	}
	for _, want := range []string{"mcp-bridge login slack", "invalid_grant", "refresh token revoked"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// Some providers answer HTTP 200 with an error object. Slack's token endpoint
// is the well-known one. Storing that as success yields an empty token and an
// opaque 401 later.
func TestErrorBodyWithHTTP200IsAnError(t *testing.T) {
	ts := newTokenServer(t, `{"ok":false,"error":"invalid_code"}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: time.Now().Add(-time.Minute).Unix()})
	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})

	if _, err := p.Token(); err == nil {
		t.Fatal("an error body returned with HTTP 200 was treated as success")
	} else if !strings.Contains(err.Error(), "invalid_code") {
		t.Errorf("error does not carry the provider's reason: %v", err)
	}
}

func TestClientAuthMethods(t *testing.T) {
	cases := []struct {
		method     string
		wantInForm []string
		wantBasic  bool
	}{
		{"", []string{"client_id", "client_secret"}, false},
		{"post", []string{"client_id", "client_secret"}, false},
		{"none", []string{"client_id"}, false},
		{"basic", nil, true},
	}
	for _, tc := range cases {
		t.Run("method="+tc.method, func(t *testing.T) {
			ts := newTokenServer(t, `{"access_token":"a","expires_in":60}`)
			path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1})
			p, _ := NewProvider(ProviderConfig{
				ServerName: "s", TokensPath: path, TokenURL: ts.URL,
				ClientID: "the-id", ClientSecret: "the-secret", ClientAuthMethod: tc.method,
			})
			if _, err := p.Token(); err != nil {
				t.Fatal(err)
			}

			form := ts.lastForm(t)
			for _, key := range tc.wantInForm {
				if form.Get(key) == "" {
					t.Errorf("form is missing %s: %v", key, form)
				}
			}
			ts.mu.Lock()
			header := ts.headers[len(ts.headers)-1]
			ts.mu.Unlock()

			user, pass, ok := (&http.Request{Header: header}).BasicAuth()
			if tc.wantBasic {
				if !ok || user != "the-id" || pass != "the-secret" {
					t.Errorf("basic auth not sent: ok=%v user=%q", ok, user)
				}
				// RFC 6749 §2.3.1: the credentials must not be sent twice.
				if form.Get("client_secret") != "" {
					t.Error("client_secret was sent in the form as well as in the header")
				}
			} else if ok {
				t.Error("basic auth was sent for a form-based method")
			}
			if tc.method == "none" && form.Get("client_secret") != "" {
				t.Error("a secret was sent for a public client")
			}
		})
	}
}

// A refresh that cannot be persisted still works for this session; it warns
// rather than failing, because the token in hand is valid.
func TestUnwritableTokenFileWarnsButSucceeds(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"fresh","expires_in":3600}`)
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.json")
	if err := SaveTokens(path, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("cannot make the directory read-only on this platform")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	var logs bytes.Buffer
	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL, Logs: &logs})
	got, err := p.Token()
	if err != nil {
		t.Fatalf("a valid refreshed token was discarded because it could not be saved: %v", err)
	}
	if got != "fresh" {
		t.Errorf("token = %q", got)
	}
	if !strings.Contains(logs.String(), "could not be saved") {
		t.Errorf("the failure to persist was not reported: %q", logs.String())
	}
}

func TestConcurrentTokenCallsAreSafe(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"fresh","expires_in":3600}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1})
	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Token(); err != nil {
				t.Errorf("Token: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestTokenResponseWithoutAccessTokenIsRejected(t *testing.T) {
	ts := newTokenServer(t, `{"token_type":"Bearer"}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: 1})
	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})

	if _, err := p.Token(); err == nil {
		t.Fatal("a response with no access_token was accepted")
	} else if !strings.Contains(err.Error(), "no access_token") {
		t.Errorf("unclear error: %v", err)
	}
}

func TestProviderSatisfiesTheTransportInterface(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "x"})
	p, err := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path})
	if err != nil {
		t.Fatal(err)
	}
	// Structural check: the transport takes anything with these two methods.
	var _ interface {
		Token() (string, error)
		Invalidate()
	} = p

	// And the stored file must remain valid JSON after use.
	if _, err := json.Marshal(p.tokens); err != nil {
		t.Fatal(err)
	}
}

// After the server refuses a credential that cannot be renewed, the error must
// say that. Reporting "no access token" instead describes only the after-effect
// of Invalidate and sends the user looking for an empty token file.
func TestRejectedNonRefreshableTokenSaysSo(t *testing.T) {
	path := storedAt(t, &Tokens{AccessToken: "revoked-token"})
	p, err := NewProvider(ProviderConfig{ServerName: "slack", TokensPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Token(); err != nil {
		t.Fatalf("the first use should succeed: %v", err)
	}

	p.Invalidate() // what the transport does after a 401

	_, err = p.Token()
	if err == nil {
		t.Fatal("a rejected credential was handed out again")
	}
	if !strings.Contains(err.Error(), "rejected by the server") {
		t.Errorf("error does not say the credential was rejected: %v", err)
	}
	if !strings.Contains(err.Error(), "mcp-bridge login slack") {
		t.Errorf("error does not name the fix: %v", err)
	}
	if strings.Contains(err.Error(), "no access token") {
		t.Errorf("error still reports the after-effect rather than the cause: %v", err)
	}
}

// A successful refresh clears the rejection, so one 401 does not poison the
// rest of the session.
func TestSuccessfulRefreshClearsTheRejection(t *testing.T) {
	ts := newTokenServer(t, `{"access_token":"fresh","expires_in":3600}`)
	path := storedAt(t, &Tokens{AccessToken: "stale", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour).Unix()})
	p, _ := NewProvider(ProviderConfig{ServerName: "s", TokensPath: path, TokenURL: ts.URL})

	p.Invalidate()
	if _, err := p.Token(); err != nil {
		t.Fatalf("refresh after a 401: %v", err)
	}
	// A second Invalidate-and-refresh must work the same way.
	p.Invalidate()
	if _, err := p.Token(); err != nil {
		t.Errorf("the session was poisoned by the first rejection: %v", err)
	}
}

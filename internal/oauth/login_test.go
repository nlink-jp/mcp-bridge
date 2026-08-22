package oauth

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProvider is an OAuth authorization server good enough to complete a real
// authorization_code flow, including verifying the PKCE challenge.
type fakeProvider struct {
	*httptest.Server

	mu            sync.Mutex
	authorizeReq  url.Values
	tokenForm     url.Values
	challenge     string
	registrations int

	// knobs
	authorizeError string // when set, /authorize redirects with an error
	noRegistration bool   // when true, metadata omits registration_endpoint
	clientSecret   string // returned by dynamic registration
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		meta := map[string]any{
			"issuer":                 p.URL,
			"authorization_endpoint": p.URL + "/authorize",
			"token_endpoint":         p.URL + "/token",
			"scopes_supported":       []string{"read", "write"},
		}
		if !p.noRegistration {
			meta["registration_endpoint"] = p.URL + "/register"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(meta)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.registrations++
		secret := p.clientSecret
		p.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"client_id": "registered-client", "client_secret": secret})
	})

	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		p.mu.Lock()
		p.authorizeReq = query
		p.challenge = query.Get("code_challenge")
		authErr := p.authorizeError
		p.mu.Unlock()

		back, err := url.Parse(query.Get("redirect_uri"))
		if err != nil {
			http.Error(w, "bad redirect_uri", http.StatusBadRequest)
			return
		}
		params := url.Values{"state": {query.Get("state")}}
		if authErr != "" {
			params.Set("error", authErr)
			params.Set("error_description", "the user said no")
		} else {
			params.Set("code", "the-auth-code")
		}
		back.RawQuery = params.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		p.mu.Lock()
		p.tokenForm = r.PostForm
		challenge := p.challenge
		p.mu.Unlock()

		// Verify PKCE for real: the verifier must hash to the challenge sent
		// with the authorization request.
		if got := codeChallenge(r.PostForm.Get("code_verifier")); got != challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"PKCE verification failed"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"granted","refresh_token":"r","token_type":"Bearer","expires_in":3600}`)
	})

	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// browserFor returns an OpenBrowser stand-in that follows the flow the way a
// browser would, including the redirect back to the loopback callback.
func browserFor(client *http.Client) func(string) error {
	return func(rawURL string) error {
		resp, err := client.Get(rawURL)
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}
}

// insecureClient trusts the ephemeral self-signed callback certificate, which
// is what clicking through the browser warning amounts to.
func insecureClient() *http.Client {
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // loopback callback only
	}
}

func loginConfig(t *testing.T, p *fakeProvider, settings Settings) LoginConfig {
	t.Helper()
	dir := t.TempDir()
	return LoginConfig{
		ServerName:    "test",
		ServerURL:     p.URL + "/mcp",
		Settings:      settings,
		TokensPath:    filepath.Join(dir, "tokens.json"),
		DiscoveryPath: filepath.Join(dir, "discovery.json"),
		HTTPClient:    p.Client(),
		Out:           &bytes.Buffer{},
		OpenBrowser:   browserFor(insecureClient()),
		Timeout:       10 * time.Second,
	}
}

// The whole flow against a pre-registered client: the case mcp-bridge exists
// for.
func TestLoginWithPreRegisteredClient(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL:     p.URL + "/authorize",
		TokenURL:         p.URL + "/token",
		ClientID:         "pre-registered",
		ClientSecret:     "the-secret",
		Scopes:           []string{"chat:write", "channels:history"},
		ClientAuthMethod: "post",
	})

	tokens, err := Login(cfg)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tokens.AccessToken != "granted" {
		t.Errorf("access token = %q", tokens.AccessToken)
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.registrations != 0 {
		t.Error("a pre-registered client triggered dynamic registration")
	}
	if got := p.authorizeReq.Get("client_id"); got != "pre-registered" {
		t.Errorf("client_id = %q", got)
	}
	if got := p.authorizeReq.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	if got := p.authorizeReq.Get("scope"); got != "chat:write channels:history" {
		t.Errorf("scope = %q", got)
	}
	if got := p.tokenForm.Get("client_secret"); got != "the-secret" {
		t.Errorf("the confidential client's secret was not sent: %q", got)
	}

	// The token must be persisted, or the next session has to log in again.
	stored, err := LoadTokens(cfg.TokensPath)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccessToken != "granted" || stored.RefreshToken != "r" {
		t.Errorf("stored tokens = %+v", stored)
	}
}

func TestLoginWithDiscoveryAndDynamicRegistration(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{}) // empty: discover everything

	tokens, err := Login(cfg)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tokens.AccessToken != "granted" {
		t.Errorf("access token = %q", tokens.AccessToken)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.registrations != 1 {
		t.Errorf("dynamic registrations = %d, want 1", p.registrations)
	}
	if got := p.authorizeReq.Get("client_id"); got != "registered-client" {
		t.Errorf("client_id = %q, want the dynamically registered one", got)
	}
	// A dynamically registered public client has no secret to send.
	if got := p.tokenForm.Get("client_secret"); got != "" {
		t.Errorf("a secret was sent for a public client: %q", got)
	}
}

// Registering a fresh client on every login litters the provider's admin
// console with single-use records. A cached registration for the same
// redirect URI is reused.
func TestDiscoveryCacheIsReusedForTheSameRedirectURI(t *testing.T) {
	p := newFakeProvider(t)
	dir := t.TempDir()
	base := loginConfig(t, p, Settings{CallbackPort: freePort(t)})
	base.TokensPath = filepath.Join(dir, "tokens.json")
	base.DiscoveryPath = filepath.Join(dir, "discovery.json")

	if _, err := Login(base); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := Login(base); err != nil {
		t.Fatalf("second login: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.registrations != 1 {
		t.Errorf("dynamic registrations = %d, want 1 — the cached client was not reused", p.registrations)
	}
}

// With an ephemeral port the redirect URI differs each time, so the cached
// client is not valid and a new one must be registered.
func TestDiscoveryCacheIsNotReusedForADifferentRedirectURI(t *testing.T) {
	p := newFakeProvider(t)
	dir := t.TempDir()
	cfg := loginConfig(t, p, Settings{}) // ephemeral port
	cfg.TokensPath = filepath.Join(dir, "tokens.json")
	cfg.DiscoveryPath = filepath.Join(dir, "discovery.json")

	if _, err := Login(cfg); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := Login(cfg); err != nil {
		t.Fatalf("second login: %v", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.registrations != 2 {
		t.Errorf("dynamic registrations = %d, want 2 — a stale client would be bound to the wrong redirect URI", p.registrations)
	}
}

// Slack rejects http:// loopback redirect URIs at app-registration time, so
// the https callback is the whole reason this tool reaches Slack at all.
func TestHTTPSCallbackUsesLocalhostAndSucceeds(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL:   p.URL + "/authorize",
		TokenURL:       p.URL + "/token",
		ClientID:       "pre-registered",
		CallbackScheme: "https",
		CallbackPort:   freePort(t),
	})
	out := &bytes.Buffer{}
	cfg.Out = out

	if _, err := Login(cfg); err != nil {
		t.Fatalf("Login over an https callback: %v", err)
	}

	p.mu.Lock()
	redirect := p.authorizeReq.Get("redirect_uri")
	p.mu.Unlock()

	if !strings.HasPrefix(redirect, "https://localhost:") {
		t.Errorf("redirect_uri = %q, want an https://localhost URI", redirect)
	}
	// The browser warning is expected, so the user is told before it appears.
	if !strings.Contains(out.String(), "not secure") {
		t.Errorf("the certificate warning was not explained:\n%s", out.String())
	}
}

func TestFixedCallbackPortIsHonoured(t *testing.T) {
	p := newFakeProvider(t)
	port := freePort(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize",
		TokenURL:     p.URL + "/token",
		ClientID:     "pre-registered",
		CallbackPort: port,
	})

	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if got := p.authorizeReq.Get("redirect_uri"); !strings.Contains(got, fmt.Sprintf(":%d/", port)) {
		t.Errorf("redirect_uri = %q, want port %d", got, port)
	}
}

// A pre-registered app declares an exact redirect URI, so a port collision has
// to be reported plainly rather than silently falling back to a random port
// the provider would reject.
func TestOccupiedCallbackPortIsReported(t *testing.T) {
	p := newFakeProvider(t)
	port := freePort(t)
	blocker, err := listenOn(port)
	if err != nil {
		t.Skip("could not hold the port for this test")
	}
	defer blocker.Close()

	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token",
		ClientID: "x", CallbackPort: port,
	})
	_, err = Login(cfg)
	if err == nil {
		t.Fatal("an occupied fixed port was accepted")
	}
	if !strings.Contains(err.Error(), "--callback-port") {
		t.Errorf("the error does not say how to change the port: %v", err)
	}
}

// The state parameter is the CSRF defence. A callback carrying the wrong one
// must not produce tokens.
func TestStateMismatchIsRejected(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token", ClientID: "x",
	})
	cfg.Timeout = 2 * time.Second
	// A "browser" that calls back with a state the flow never issued.
	cfg.OpenBrowser = func(rawURL string) error {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return err
		}
		redirect := parsed.Query().Get("redirect_uri")
		resp, err := insecureClient().Get(redirect + "?code=stolen&state=wrong-state")
		if err != nil {
			return err
		}
		resp.Body.Close()
		return nil
	}

	_, err := Login(cfg)
	if err == nil {
		t.Fatal("a callback with the wrong state produced tokens")
	}
	if !strings.Contains(err.Error(), "state") {
		t.Errorf("error does not identify the cause: %v", err)
	}
}

func TestProviderDeclinedAuthorization(t *testing.T) {
	p := newFakeProvider(t)
	p.authorizeError = "access_denied"
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token", ClientID: "x",
	})

	_, err := Login(cfg)
	if err == nil {
		t.Fatal("a refused authorization was treated as success")
	}
	if !strings.Contains(err.Error(), "access_denied") || !strings.Contains(err.Error(), "the user said no") {
		t.Errorf("error does not carry the provider's reason: %v", err)
	}
}

func TestLoginTimesOut(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token", ClientID: "x",
	})
	cfg.Timeout = 200 * time.Millisecond
	cfg.OpenBrowser = func(string) error { return nil } // the user never finishes

	_, err := Login(cfg)
	if err == nil {
		t.Fatal("Login waited forever")
	}
	if !strings.Contains(err.Error(), "callback") {
		t.Errorf("unclear timeout error: %v", err)
	}
}

// When a browser cannot be opened the URL is still printed, so the login is
// completable by hand.
func TestBrowserFailureStillPrintsTheURL(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize", TokenURL: p.URL + "/token", ClientID: "x",
	})
	out := &bytes.Buffer{}
	cfg.Out = out
	cfg.Timeout = 200 * time.Millisecond
	cfg.OpenBrowser = func(string) error { return fmt.Errorf("no browser here") }

	_, _ = Login(cfg)

	if !strings.Contains(out.String(), p.URL+"/authorize") {
		t.Errorf("the authorization URL was not printed:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "no browser here") {
		t.Errorf("the browser failure was not reported:\n%s", out.String())
	}
}

// A provider without dynamic registration is exactly mcp-bridge's audience,
// so the error has to point at manual configuration rather than just failing.
func TestNoDynamicRegistrationPointsAtManualSetup(t *testing.T) {
	p := newFakeProvider(t)
	p.noRegistration = true
	cfg := loginConfig(t, p, Settings{})

	_, err := Login(cfg)
	if err == nil {
		t.Fatal("registration succeeded against a server that does not support it")
	}
	for _, want := range []string{"oauth.clientId", "Register an OAuth app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// extraParams must not be able to overwrite the parameters the flow's
// security rests on.
func TestExtraParamsCannotOverrideProtocolParameters(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize",
		TokenURL:     p.URL + "/token",
		ClientID:     "real-client",
		ExtraParams: map[string]string{
			"audience":              "https://api.example.com",
			"state":                 "attacker-state",
			"code_challenge_method": "plain",
			"client_id":             "attacker-client",
		},
	})

	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if got := p.authorizeReq.Get("audience"); got != "https://api.example.com" {
		t.Errorf("a legitimate extra parameter was dropped: %q", got)
	}
	if got := p.authorizeReq.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method was overridden to %q", got)
	}
	if got := p.authorizeReq.Get("client_id"); got != "real-client" {
		t.Errorf("client_id was overridden to %q", got)
	}
	if p.authorizeReq.Get("state") == "attacker-state" {
		t.Error("the CSRF state was overridden by extraParams")
	}
}

// An authorize URL that already carries query parameters must keep them.
func TestAuthorizeURLPreservesExistingQuery(t *testing.T) {
	got, err := authorizeURL(Settings{
		AuthorizeURL: "https://auth.example.com/authorize?tenant=acme",
		ClientID:     "id",
	}, "http://127.0.0.1:1234/callback", "st", "ch")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(got)
	if parsed.Query().Get("tenant") != "acme" {
		t.Errorf("existing query parameter lost: %s", got)
	}
	if parsed.Query().Get("response_type") != "code" {
		t.Errorf("response_type missing: %s", got)
	}
}

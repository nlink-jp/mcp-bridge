package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/oauth"
)

// entraLike is an MCP server and its authorization server in one, shaped the
// way discovery sees a server protected by Microsoft Entra ID: a challenge
// pointing at protected-resource metadata, an issuer with a path whose
// metadata is only at the appended location, and no registration endpoint.
type entraLike struct {
	*httptest.Server

	mu         sync.Mutex
	authorize  url.Values
	tokenForms []url.Values

	scopes []string // what the server's metadata lists
}

func newEntraLike(t *testing.T) *entraLike {
	t.Helper()
	e := &entraLike{scopes: []string{"api://mcp-mail/.default"}}
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, doc any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}

	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+e.URL+`/prm"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/prm", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"resource":              e.URL + "/mcp",
			"authorization_servers": []string{e.URL + "/tenant-id/v2.0"},
			"scopes_supported":      e.scopes,
		})
	})
	mux.HandleFunc("/tenant-id/v2.0/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 e.URL + "/tenant-id/v2.0",
			"authorization_endpoint": e.URL + "/authorize",
			"token_endpoint":         e.URL + "/token",
			"scopes_supported":       []string{"openid", "profile", "email", "offline_access"},
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		e.mu.Lock()
		e.authorize = q
		e.mu.Unlock()
		back, _ := url.Parse(q.Get("redirect_uri"))
		back.RawQuery = url.Values{"state": {q.Get("state")}, "code": {"the-code"}}.Encode()
		http.Redirect(w, r, back.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		e.mu.Lock()
		e.tokenForms = append(e.tokenForms, r.PostForm)
		e.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"fresh-token","refresh_token":"next-refresh","token_type":"Bearer","expires_in":3600}`)
	})

	e.Server = httptest.NewServer(mux)
	t.Cleanup(e.Close)
	return e
}

func (e *entraLike) lastTokenForm(t *testing.T) url.Values {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.tokenForms) == 0 {
		t.Fatal("the token endpoint was never called")
	}
	return e.tokenForms[len(e.tokenForms)-1]
}

// followInBrowser stands in for the browser: it follows the authorization
// request through to the loopback callback.
func followInBrowser(rawURL string) error {
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(rawURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func unusedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// The configuration from the report, through the command itself: a client ID,
// the localhost callback, and nothing else. This is also where a key that
// validates but never reaches the login would show.
func TestLoginCommandWithAPreRegisteredClient(t *testing.T) {
	e := newEntraLike(t)
	port := unusedPort(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"m365-mail":{"url":%q,"oauth":{
		"clientId":"entra-client","clientAuthMethod":"none","callbackHost":"localhost","callbackPort":%d}}}}`,
		e.URL+"/mcp", port))

	var out bytes.Buffer
	if err := Login(LoginOptions{ConfigPath: cfg, Server: "m365-mail", Out: &out, OpenBrowser: followInBrowser}); err != nil {
		t.Fatalf("Login: %v\n%s", err, out.String())
	}

	if got, want := e.authorize.Get("redirect_uri"), fmt.Sprintf("http://localhost:%d/callback", port); got != want {
		t.Errorf("redirect_uri = %q, want %q (callbackHost did not reach the login)", got, want)
	}
	if got := e.authorize.Get("scope"); got != "api://mcp-mail/.default offline_access" {
		t.Errorf("scope = %q", got)
	}
	form := e.lastTokenForm(t)
	if form.Get("client_id") != "entra-client" || form.Has("client_secret") {
		t.Errorf("token request is not the public client's: %v", form)
	}
	if !strings.Contains(out.String(), "Logged in to m365-mail.") {
		t.Errorf("output does not report the login:\n%s", out.String())
	}

	// And `list` shows it as any other configured client.
	var listed bytes.Buffer
	if err := List(ListOptions{ConfigPath: cfg, Out: &listed}); err != nil {
		t.Fatal(err)
	}
	if fields := strings.Fields(strings.Split(listed.String(), "\n")[1]); len(fields) < 3 || fields[1] != "oauth" {
		t.Errorf("list row = %q, want auth mode \"oauth\"", fields)
	}
}

// A session for a pre-registered client refreshes at the endpoint the login
// stored, as the client the configuration names — and a secret left in the
// stored result by an earlier dynamic registration is not sent with it.
func TestSessionRefreshesWithTheConfiguredClient(t *testing.T) {
	e := newEntraLike(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"m365-mail":{"url":%q,"oauth":{"clientId":"entra-client"}}}}`, e.URL+"/mcp"))
	srv, err := loadServer(cfg, "m365-mail")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := config.EnsureStateDir("m365-mail"); err != nil {
		t.Fatal(err)
	}
	discoveryPath, _ := config.DiscoveryPath("m365-mail")
	stale := fmt.Sprintf(`{"authorize_url":%q,"token_url":%q,"client_id":"registered-earlier","client_secret":"its-secret"}`,
		e.URL+"/authorize", e.URL+"/token")
	if err := os.WriteFile(discoveryPath, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	tokensPath, _ := config.TokensPath("m365-mail")
	expired := &oauth.Tokens{AccessToken: "old", RefreshToken: "the-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if err := oauth.SaveTokens(tokensPath, expired); err != nil {
		t.Fatal(err)
	}

	provider, err := oauthProvider(srv, "m365-mail", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	token, err := provider.Token()
	if err != nil {
		t.Fatal(err)
	}
	if token != "fresh-token" {
		t.Errorf("token = %q, want the refreshed one", token)
	}

	form := e.lastTokenForm(t)
	for param, want := range map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": "the-refresh",
		"client_id":     "entra-client",
	} {
		if got := form.Get(param); got != want {
			t.Errorf("refresh %s = %q, want %q", param, got, want)
		}
	}
	if form.Has("client_secret") {
		t.Errorf("another client's secret was sent: %q", form.Get("client_secret"))
	}
	if form.Has("scope") {
		t.Errorf("the refresh request carries scope=%q", form.Get("scope"))
	}
}

// Before the first login there is no stored endpoint to refresh at, and the
// error names the command that creates one.
func TestPreRegisteredClientWithoutALoginNamesTheLoginCommand(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"m365-mail":{"url":"https://e.example.com/mcp","oauth":{"clientId":"entra-client"}}}}`)
	err := Run(RunOptions{ConfigPath: cfg, Server: "m365-mail", In: strings.NewReader(""), Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "mcp-bridge login m365-mail") {
		t.Errorf("error does not name the fix: %v", err)
	}
}

// Several servers may share one callback port because only a login uses it. A
// session starts and runs with the port held by something else.
func TestSessionDoesNotUseTheCallbackPort(t *testing.T) {
	srv, _ := mockMCP(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{
		"mail":{"url":%q,"oauth":{"clientId":"entra-client","callbackHost":"localhost","callbackPort":%d}},
		"calendar":{"url":%q,"oauth":{"clientId":"entra-client","callbackHost":"localhost","callbackPort":%d}}}}`,
		srv.URL, port, srv.URL, port))

	for _, name := range []string{"mail", "calendar"} {
		if _, err := config.EnsureStateDir(name); err != nil {
			t.Fatal(err)
		}
		discoveryPath, _ := config.DiscoveryPath(name)
		if err := os.WriteFile(discoveryPath, []byte(`{"token_url":"https://login.example.com/token"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		tokensPath, _ := config.TokensPath(name)
		if err := oauth.SaveTokens(tokensPath, &oauth.Tokens{AccessToken: name + "-token"}); err != nil {
			t.Fatal(err)
		}
		lines := bridgeSession(t, cfg, name, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")
		if len(lines) != 1 || !strings.Contains(lines[0], `"serverInfo"`) {
			t.Errorf("%s: session did not answer: %v", name, lines)
		}
	}
}

// A stored token endpoint was discovered once, and is held to the condition
// discovery applies. An earlier version stored whatever it was handed; a
// refresh sent there would carry the refresh token and the client's secret
// over http on behalf of an https server.
func TestSessionRefusesAStoredHTTPTokenEndpointForAnHTTPSServer(t *testing.T) {
	var hits int
	var mu sync.Mutex
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"leaked","token_type":"Bearer","expires_in":3600}`)
	}))
	defer plain.Close()

	cfg := writeConfig(t, `{"servers":{"s":{"url":"https://mcp.example.com/mcp","oauth":{}}}}`)
	if _, err := config.EnsureStateDir("s"); err != nil {
		t.Fatal(err)
	}
	discoveryPath, _ := config.DiscoveryPath("s")
	// The stored form of an earlier version: no server_url, nothing checked.
	old := fmt.Sprintf(`{"authorize_url":"https://auth.example.com/a","token_url":%q,"client_id":"old-dcr","client_secret":"old-secret"}`,
		plain.URL+"/token")
	if err := os.WriteFile(discoveryPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	tokensPath, _ := config.TokensPath("s")
	expired := &oauth.Tokens{AccessToken: "old", RefreshToken: "the-refresh", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	if err := oauth.SaveTokens(tokensPath, expired); err != nil {
		t.Fatal(err)
	}

	err := Run(RunOptions{ConfigPath: cfg, Server: "s", In: strings.NewReader(""), Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("the session started with an http token endpoint for an https server")
	}
	for _, want := range []string{"is not https", "mcp-bridge login s"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q: %v", want, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("%d requests reached the http token endpoint", hits)
	}
}

// For a server configured with an http url the same stored endpoint is what
// discovery would accept today, and keeps working.
func TestSessionKeepsAStoredHTTPTokenEndpointForAnHTTPServer(t *testing.T) {
	e := newEntraLike(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"s":{"url":%q,"oauth":{"clientId":"entra-client"}}}}`, e.URL+"/mcp"))
	srv, err := loadServer(cfg, "s")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.EnsureStateDir("s"); err != nil {
		t.Fatal(err)
	}
	discoveryPath, _ := config.DiscoveryPath("s")
	if err := os.WriteFile(discoveryPath, []byte(fmt.Sprintf(`{"token_url":%q}`, e.URL+"/token")), 0o600); err != nil {
		t.Fatal(err)
	}
	tokensPath, _ := config.TokensPath("s")
	if err := oauth.SaveTokens(tokensPath, &oauth.Tokens{AccessToken: "stored"}); err != nil {
		t.Fatal(err)
	}
	if _, err := oauthProvider(srv, "s", &bytes.Buffer{}); err != nil {
		t.Errorf("a stored http endpoint for an http server was refused: %v", err)
	}
}

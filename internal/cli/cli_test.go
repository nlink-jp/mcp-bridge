package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nlink-jp/mcp-bridge/internal/config"
	"github.com/nlink-jp/mcp-bridge/internal/oauth"
)

// writeConfig places a config file in an isolated XDG_CONFIG_HOME and returns
// its path. Isolating the environment keeps tests off the developer's real
// configuration and token files.
func writeConfig(t *testing.T, content string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	dir := filepath.Join(base, "mcp-bridge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// mockMCP is a minimal MCP server: it answers initialize and tools/list over
// the Streamable HTTP transport and records the headers it was called with.
func mockMCP(t *testing.T) (*httptest.Server, func() []http.Header) {
	t.Helper()
	var mu sync.Mutex
	var headers []http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()

		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &msg)

		if len(msg.ID) == 0 {
			// A notification: nothing to answer.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"mock","version":"1"}}}`, msg.ID)
		case "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"echo"}]}}`, msg.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"method not found"}}`, msg.ID)
		}
	}))
	t.Cleanup(srv.Close)

	return srv, func() []http.Header {
		mu.Lock()
		defer mu.Unlock()
		return headers
	}
}

// bridgeSession runs one full session and returns the lines written to stdout.
func bridgeSession(t *testing.T, configPath, server, input string) []string {
	t.Helper()
	var out, logs bytes.Buffer
	err := Run(RunOptions{
		ConfigPath: configPath,
		Server:     server,
		In:         strings.NewReader(input),
		Out:        &out,
		Logs:       &logs,
	})
	if err != nil {
		t.Fatalf("Run: %v (logs: %s)", err, logs.String())
	}
	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// The end-to-end path: a client speaking stdio reaches a real HTTP MCP server
// and its answers come back.
func TestBridgeRoundTrip(t *testing.T) {
	srv, _ := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"mock":{"url":%q}}}`, srv.URL))

	lines := bridgeSession(t, cfg, "mock",
		`{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")

	if len(lines) != 2 {
		t.Fatalf("client received %d messages, want 2: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"serverInfo"`) {
		t.Errorf("initialize response = %s", lines[0])
	}
	if !strings.Contains(lines[1], `"echo"`) {
		t.Errorf("tools/list response = %s", lines[1])
	}
	// Every line the client sees must be a single valid JSON-RPC message.
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("invalid JSON on stdout: %q", line)
		}
	}
}

func TestStaticHeadersReachTheServer(t *testing.T) {
	srv, seen := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"mock":{"url":%q,"headers":{"Authorization":"Bearer static-token","X-Tenant":"acme"}}}}`, srv.URL))

	bridgeSession(t, cfg, "mock", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")

	headers := seen()
	if len(headers) == 0 {
		t.Fatal("the server was never called")
	}
	if got := headers[0].Get("Authorization"); got != "Bearer static-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := headers[0].Get("X-Tenant"); got != "acme" {
		t.Errorf("X-Tenant = %q", got)
	}
}

// A server the client asks for but the config does not define is the common
// typo. The error lists what is configured, because that is the fix.
func TestUnknownServerListsAlternatives(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"slack":{"url":"https://e.example.com/mcp"},"aws":{"url":"https://e.example.com/mcp"}}}`)
	err := Run(RunOptions{ConfigPath: cfg, Server: "slak", In: strings.NewReader(""), Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("an unknown server was accepted")
	}
	if !strings.Contains(err.Error(), "aws, slack") {
		t.Errorf("error does not list the configured servers: %v", err)
	}
}

// An OAuth server with no stored login must fail with the command that fixes
// it, not with a missing-file error about a path the user never created.
func TestOAuthWithoutLoginNamesTheLoginCommand(t *testing.T) {
	cases := map[string]string{
		"configured": `{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id"}}`,
		"discover":   `{"url":"https://e.example.com/mcp","oauth":{}}`,
	}
	for name, server := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := writeConfig(t, `{"servers":{"s":`+server+`}}`)
			err := Run(RunOptions{ConfigPath: cfg, Server: "s", In: strings.NewReader(""), Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
			if err == nil {
				t.Fatal("a server with no stored login was accepted")
			}
			if !strings.Contains(err.Error(), "mcp-bridge login s") {
				t.Errorf("error does not name the fix: %v", err)
			}
		})
	}
}

// A stored login lets the session start, and the access token reaches the
// server as a Bearer credential.
func TestStoredLoginAuthenticatesTheSession(t *testing.T) {
	srv, seen := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"mock":{"url":%q,"oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id"}}}}`, srv.URL))

	tokensPath, err := config.TokensPath("mock")
	if err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveTokens(tokensPath, &oauth.Tokens{AccessToken: "stored-token"}); err != nil {
		t.Fatal(err)
	}

	bridgeSession(t, cfg, "mock", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")

	headers := seen()
	if len(headers) == 0 {
		t.Fatal("the server was never called")
	}
	if got := headers[0].Get("Authorization"); got != "Bearer stored-token" {
		t.Errorf("Authorization = %q, want the stored access token", got)
	}
}

// tokenCommand runs the user's own credential tool and sends its output.
func TestTokenCommandSuppliesTheBearerToken(t *testing.T) {
	srv, seen := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"mock":{"url":%q,"tokenCommand":{"command":"printf","args":["ya29.from-command"]}}}}`, srv.URL))

	bridgeSession(t, cfg, "mock", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n")

	headers := seen()
	if len(headers) == 0 {
		t.Fatal("the server was never called")
	}
	if got := headers[0].Get("Authorization"); got != "Bearer ya29.from-command" {
		t.Errorf("Authorization = %q, want the token the command printed", got)
	}
}

// Logging in to a server that does not use OAuth would open a browser for
// nothing, so it is refused with the reason.
func TestLoginRefusesNonOAuthServers(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"plain":{"url":"https://e.example.com/mcp"}}}`)
	err := Login(LoginOptions{ConfigPath: cfg, Server: "plain", Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("a login was started for a server with no OAuth")
	}
	if !strings.Contains(err.Error(), "does not use OAuth") {
		t.Errorf("unclear error: %v", err)
	}
}

func TestLogoutRemovesTheStoredTokens(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"slack":{"url":"https://mcp.slack.com/mcp","oauth":{}}}}`)
	tokensPath, err := config.TokensPath("slack")
	if err != nil {
		t.Fatal(err)
	}
	if err := oauth.SaveTokens(tokensPath, &oauth.Tokens{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Logout(LogoutOptions{ConfigPath: cfg, Server: "slack", Out: &out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokensPath); !os.IsNotExist(err) {
		t.Error("the token file survived logout")
	}
	if !strings.Contains(out.String(), "Logged out") {
		t.Errorf("logout said nothing useful: %q", out.String())
	}

	// Logging out twice is not an error, and says so plainly.
	out.Reset()
	if err := Logout(LogoutOptions{ConfigPath: cfg, Server: "slack", Out: &out}); err != nil {
		t.Errorf("second logout failed: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("a second logout was not reported clearly: %q", out.String())
	}
}

// The discovery cache holds the client registered with the provider, not the
// user's credentials. Deleting it would create a second client record at the
// next login for no benefit.
func TestLogoutKeepsTheDiscoveryCache(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"slack":{"url":"https://mcp.slack.com/mcp","oauth":{}}}}`)
	tokensPath, _ := config.TokensPath("slack")
	discoveryPath, _ := config.DiscoveryPath("slack")
	if err := oauth.SaveTokens(tokensPath, &oauth.Tokens{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(discoveryPath, []byte(`{"client_id":"registered"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Logout(LogoutOptions{ConfigPath: cfg, Server: "slack", Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(discoveryPath); err != nil {
		t.Errorf("the discovery cache was deleted by logout: %v", err)
	}
}

func TestInspectReportsServerAndTools(t *testing.T) {
	srv, _ := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"mock":{"url":%q}}}`, srv.URL))

	var out bytes.Buffer
	if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "mock", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	text := out.String()
	for _, want := range []string{"mock", "2025-06-18", "1 tool(s)", "echo"} {
		if !strings.Contains(text, want) {
			t.Errorf("inspect output is missing %q:\n%s", want, text)
		}
	}
}

// Inspect is the command that answers "is my configuration right?", so its
// authentication failures must carry the fix.
func TestInspectSurfacesAuthenticationProblems(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"slack":{"url":"https://mcp.slack.com/mcp","oauth":{}}}}`)
	err := Inspect(InspectOptions{ConfigPath: cfg, Server: "slack", Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("inspect succeeded with no stored login")
	}
	if !strings.Contains(err.Error(), "mcp-bridge login slack") {
		t.Errorf("error does not name the fix: %v", err)
	}
}

func TestListShowsEveryServer(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{
		"plain":{"url":"https://plain.example.com/mcp"},
		"static":{"url":"https://static.example.com/mcp","headers":{"Authorization":"Bearer x"}},
		"slack":{"url":"https://mcp.slack.com/mcp","oauth":{}}
	}}`)

	var out bytes.Buffer
	if err := List(ListOptions{ConfigPath: cfg, Out: &out}); err != nil {
		t.Fatal(err)
	}
	text := out.String()

	for _, want := range []string{"NAME", "AUTH", "STATE", "URL", "plain", "static", "slack", "none", "static-headers", "oauth-discover"} {
		if !strings.Contains(text, want) {
			t.Errorf("listing is missing %q:\n%s", want, text)
		}
	}
	// Servers are listed in sorted order so the output is stable between runs.
	if strings.Index(text, "plain") > strings.Index(text, "slack") {
		t.Errorf("servers are not sorted:\n%s", text)
	}
}

// The state column answers "do I need to log in?", which is the question a
// user runs list to settle.
func TestListReportsLoginState(t *testing.T) {
	cfg := writeConfig(t, `{"servers":{"slack":{"url":"https://mcp.slack.com/mcp","oauth":{}},"plain":{"url":"https://plain.example.com/mcp"}}}`)

	var before bytes.Buffer
	if err := List(ListOptions{ConfigPath: cfg, Out: &before}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before.String(), "not logged in") {
		t.Errorf("a server with no token was not reported as logged out:\n%s", before.String())
	}

	tokens, err := config.TokensPath("slack")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(tokens), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokens, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var after bytes.Buffer
	if err := List(ListOptions{ConfigPath: cfg, Out: &after}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(after.String(), "logged in") || strings.Contains(after.String(), "not logged in") {
		t.Errorf("a stored token was not reported:\n%s", after.String())
	}
	// A server with no login to do says so with a dash rather than claiming a
	// state it cannot have.
	for _, line := range strings.Split(after.String(), "\n") {
		if strings.HasPrefix(line, "plain") && !strings.Contains(line, "-") {
			t.Errorf("a server needing no login was given a login state: %q", line)
		}
	}
}

func TestListRejectsAMissingConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	err := List(ListOptions{Out: &bytes.Buffer{}})
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("unhelpful error for a missing config: %v", err)
	}
}

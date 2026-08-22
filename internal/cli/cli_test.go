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

// The unimplemented authentication modes must be rejected by name. Failing
// later as an unexplained 401 would send the user hunting in the wrong place.
func TestUnimplementedAuthModesAreNamed(t *testing.T) {
	cases := []struct {
		name, server, want string
	}{
		{
			"oauth configured",
			`{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id"}}`,
			"OAuth",
		},
		{"oauth discover", `{"url":"https://e.example.com/mcp","oauth":{}}`, "OAuth"},
		{"token command", `{"url":"https://e.example.com/mcp","tokenCommand":{"command":"gcloud"}}`, "tokenCommand"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := writeConfig(t, `{"servers":{"s":`+tc.server+`}}`)
			err := Run(RunOptions{ConfigPath: cfg, Server: "s", In: strings.NewReader(""), Out: &bytes.Buffer{}, Logs: &bytes.Buffer{}})
			if err == nil {
				t.Fatal("an unimplemented auth mode was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "not implemented") {
				t.Errorf("error does not say what is missing: %v", err)
			}
		})
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

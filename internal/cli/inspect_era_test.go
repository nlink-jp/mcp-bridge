package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// modernOnlyMCP is a Streamable HTTP server that speaks only MCP 2026-07-28,
// written from the specification text: every request carries its version in
// _meta and in MCP-Protocol-Version, which must agree; server/discover answers
// a DiscoverResult; initialize is unknown. With requireToken it answers 401
// to a request without "Bearer t".
func modernOnlyMCP(t *testing.T, requireToken bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requireToken && r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Meta map[string]json.RawMessage `json:"_meta"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &msg)
		var version string
		_ = json.Unmarshal(msg.Params.Meta["io.modelcontextprotocol/protocolVersion"], &version)
		w.Header().Set("Content-Type", "application/json")
		fail := func(code int, message string) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%q}}`, msg.ID, code, message)
		}
		switch {
		case version == "":
			fail(-32601, "this server speaks only 2026-07-28")
		case r.Header.Get("MCP-Protocol-Version") != version || r.Header.Get("Mcp-Method") != msg.Method:
			fail(-32020, "headers and body disagree")
		case version != "2026-07-28":
			fail(-32022, "Unsupported protocol version")
		case msg.Method == "server/discover":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"_meta":{"io.modelcontextprotocol/serverInfo":{"name":"modern-srv","version":"9"}},"ttlMs":1000,"cacheScope":"private"}}`, msg.ID)
		case msg.Method == "tools/list":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"resultType":"complete","ttlMs":1000,"cacheScope":"private","tools":[{"name":"modern_tool","description":"d","inputSchema":{"type":"object"}}]}}`, msg.ID)
		default:
			fail(-32601, "method not found")
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestInspectOpensAModernServerInTheModernEra(t *testing.T) {
	srv := modernOnlyMCP(t, false)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"m":{"url":%q}}}`, srv.URL))

	var out bytes.Buffer
	if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "m", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	for _, want := range []string{"modern-srv 9", "2026-07-28 (modern)", "1 tool(s)", "modern_tool"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("inspect output is missing %q:\n%s", want, out.String())
		}
	}
}

func TestInspectNamesTheLegacyEra(t *testing.T) {
	srv, _ := mockMCP(t)
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"mock":{"url":%q}}}`, srv.URL))

	var out bytes.Buffer
	if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "mock", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.Contains(out.String(), "2025-06-18 (legacy)") {
		t.Errorf("inspect did not name the legacy era:\n%s", out.String())
	}
}

// The no-credential check opens the server the same way, so against a
// modern-only server it still tells "answers anyone" from "needed the
// credential" (ADR-0008 §9). The handshake it used before was unknown to such
// a server, so inspect could report "credential accepted" untested.
func TestInspectCredentialCheckWorksAgainstAModernServer(t *testing.T) {
	cases := []struct {
		name          string
		requireToken  bool
		want, notWant string
	}{
		{"answers anyone", false, "does not confirm", "credential accepted"},
		{"needs the credential", true, "credential accepted", "does not confirm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := modernOnlyMCP(t, c.requireToken)
			cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"m":{"url":%q,"headers":{"Authorization":"Bearer t"}}}}`, srv.URL))
			var out bytes.Buffer
			if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "m", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
				t.Fatalf("Inspect: %v", err)
			}
			if !strings.Contains(out.String(), c.want) || strings.Contains(out.String(), c.notWant) {
				t.Errorf("auth report (want %q, not %q):\n%s", c.want, c.notWant, out.String())
			}
		})
	}
}

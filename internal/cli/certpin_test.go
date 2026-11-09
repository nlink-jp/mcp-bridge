package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockMCPTLS serves the mock over TLS with a certificate no client trusts, and
// returns the fingerprint that pins it.
func mockMCPTLS(t *testing.T) (*httptest.Server, string, func() []http.Header) {
	t.Helper()
	srv, headers := startMockMCP(t, func(handler http.Handler) *httptest.Server {
		s := httptest.NewUnstartedServer(handler)
		s.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are expected
		s.StartTLS()
		return s
	})
	sum := sha256.Sum256(srv.Certificate().Raw)
	return srv, hex.EncodeToString(sum[:]), headers
}

const pinnedInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"

// The configured key has to reach the transport: the path from config.json to
// a working session, with the credential arriving.
func TestPinnedServerCarriesASession(t *testing.T) {
	srv, pin, headers := mockMCPTLS(t)
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"device":{"url":%q,"headers":{"Authorization":"Bearer device-key"},"tlsCertSha256":%q}}}`, srv.URL, pin))

	lines := bridgeSession(t, cfg, "device", pinnedInitialize)
	if len(lines) != 1 || !strings.Contains(lines[0], `"serverInfo"`) {
		t.Fatalf("output = %q", lines)
	}
	seen := headers()
	if len(seen) == 0 || seen[0].Get("Authorization") != "Bearer device-key" {
		t.Errorf("the credential did not arrive: %v", seen)
	}
}

// An MCP client sees only the JSON-RPC stream, so the reason a certificate was
// refused has to arrive there — and the credential must not.
func TestPinMismatchReachesTheClientAndNotTheServer(t *testing.T) {
	srv, _, headers := mockMCPTLS(t)
	wrong := strings.Repeat("ab", 32)
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"device":{"url":%q,"headers":{"Authorization":"Bearer device-key"},"tlsCertSha256":%q}}}`, srv.URL, wrong))

	lines := bridgeSession(t, cfg, "device", pinnedInitialize)
	if len(lines) != 1 || !strings.Contains(lines[0], `"error"`) || !strings.Contains(lines[0], "tlsCertSha256") {
		t.Fatalf("output = %q", lines)
	}
	if seen := headers(); len(seen) != 0 {
		t.Errorf("%d request(s) reached a server that failed the pin", len(seen))
	}
}

// inspect probes without a credential to learn whether the credential was
// tested. The probe opens its own connection; if that one ignored the pin it
// would fail on the certificate, and inspect would call an untested credential
// accepted.
func TestInspectProbeUsesThePin(t *testing.T) {
	srv, pin, _ := mockMCPTLS(t) // answers initialize to anyone
	cfg := writeConfig(t, fmt.Sprintf(
		`{"servers":{"device":{"url":%q,"headers":{"Authorization":"Bearer device-key"},"tlsCertSha256":%q}}}`, srv.URL, pin))

	var out bytes.Buffer
	if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "device", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "does not confirm") || strings.Contains(text, "credential accepted") {
		t.Errorf("inspect overclaimed on an untested credential:\n%s", text)
	}
}

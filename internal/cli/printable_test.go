package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrintable(t *testing.T) {
	cases := map[string]string{
		"plain text":                            "plain text",
		"two\nlines\tand a tab":                 "two\nlines\tand a tab",
		"日本語 and émoji 🙂":                       "日本語 and émoji 🙂",
		"\x1b[2J\x1b[Hcleared":                  "[2J[Hcleared",
		"title\x1b]0;owned\x07 set":             "title]0;owned set",
		"carriage\rreturn":                      "carriagereturn",
		"back\b\b\b\bspace":                     "backspace",
		"c1 \u009b31m control":                  "c1 31m control",
		"nul\x00byte and del\x7f":               "nulbyte and del",
		"scope api://x/.default offline_access": "scope api://x/.default offline_access",
	}
	for in, want := range cases {
		if got := Printable(in); got != want {
			t.Errorf("Printable(%q) = %q, want %q", in, got, want)
		}
	}
}

// What a server says about itself reaches the terminal with its control
// characters gone: here a scope name carrying an escape sequence that would
// clear the screen.
func TestLoginOutputCarriesNoControlCharactersFromTheServer(t *testing.T) {
	e := newEntraLike(t)
	e.scopes = []string{"mail\x1b[2J.read"}
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"s":{"url":%q,"oauth":{"clientId":"entra-client","callbackPort":%d}}}}`,
		e.URL+"/mcp", unusedPort(t)))

	var out bytes.Buffer
	if err := Login(LoginOptions{ConfigPath: cfg, Server: "s", Out: &out, OpenBrowser: followInBrowser}); err != nil {
		t.Fatalf("Login: %v\n%s", err, out.String())
	}
	if strings.ContainsRune(out.String(), '\x1b') {
		t.Errorf("an escape character reached the output: %q", out.String())
	}
	if !strings.Contains(out.String(), "Scopes: mail[2J.read offline_access") {
		t.Errorf("the scope line is missing or altered beyond its control characters:\n%s", out.String())
	}
}

func TestInspectOutputCarriesNoControlCharactersFromTheServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		w.Header().Set("Content-Type", "application/json")
		switch msg.Method {
		case "initialize":
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"evil\u001b[2J","version":"1"}}}`, msg.ID)
		default:
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{"tools":[{"name":"tool\u001b]0;x\u0007"}]}}`, msg.ID)
		}
	}))
	defer srv.Close()
	cfg := writeConfig(t, fmt.Sprintf(`{"servers":{"s":{"url":%q}}}`, srv.URL))

	var out bytes.Buffer
	if err := Inspect(InspectOptions{ConfigPath: cfg, Server: "s", Out: &out, Logs: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") {
		t.Errorf("a control character reached the output: %q", out.String())
	}
	if !strings.Contains(out.String(), "evil[2J") {
		t.Errorf("the server name is missing:\n%s", out.String())
	}
}

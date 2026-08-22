package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts content in a temp file and returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustLoad(t *testing.T, content string) *File {
	t.Helper()
	f, err := Load(write(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return f
}

func loadErr(t *testing.T, content string) string {
	t.Helper()
	_, err := Load(write(t, content))
	if err == nil {
		t.Fatal("expected an error, got none")
	}
	return err.Error()
}

func TestLoadMinimal(t *testing.T) {
	f := mustLoad(t, `{"servers":{"plain":{"url":"https://mcp.example.com/mcp"}}}`)
	srv, err := f.Server("plain")
	if err != nil {
		t.Fatal(err)
	}
	if srv.URL != "https://mcp.example.com/mcp" {
		t.Errorf("url = %q", srv.URL)
	}
	if got := srv.AuthMode(); got != AuthNone {
		t.Errorf("AuthMode = %q, want %q", got, AuthNone)
	}
}

// The predecessor decoded one config file strictly and another loosely, so a
// typo was fatal in one file and invisible in the other. Here there is one
// file and it is strict.
func TestUnknownKeyIsRejected(t *testing.T) {
	// "callbackSchema" for "callbackScheme" is the exact typo that silently
	// disabled https callbacks in the predecessor.
	msg := loadErr(t, `{"servers":{"s":{"url":"https://e.example.com/mcp",
		"oauth":{"authorizeUrl":"https://a.example.com/authorize",
		"tokenUrl":"https://a.example.com/token","clientId":"id",
		"callbackSchema":"https"}}}}`)
	if !strings.Contains(msg, "callbackSchema") {
		t.Errorf("error does not name the offending key: %s", msg)
	}
}

func TestUnknownTopLevelKeyIsRejected(t *testing.T) {
	msg := loadErr(t, `{"telemetry":{"otlp":{}},"servers":{"s":{"url":"https://e.example.com/mcp"}}}`)
	if !strings.Contains(msg, "telemetry") {
		t.Errorf("error does not name the offending key: %s", msg)
	}
}

func TestAuthModeSelection(t *testing.T) {
	cases := []struct {
		name, server string
		want         AuthMode
	}{
		{"none", `{"url":"https://e.example.com/mcp"}`, AuthNone},
		{"headers", `{"url":"https://e.example.com/mcp","headers":{"Authorization":"Bearer x"}}`, AuthStaticHeaders},
		{"tokenCommand", `{"url":"https://e.example.com/mcp","tokenCommand":{"command":"gcloud"}}`, AuthTokenCommand},
		{"discover", `{"url":"https://e.example.com/mcp","oauth":{}}`, AuthOAuthDiscover},
		{"configured", `{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id"}}`, AuthOAuthConfigured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := mustLoad(t, `{"servers":{"s":`+tc.server+`}}`)
			if got := f.Servers["s"].AuthMode(); got != tc.want {
				t.Errorf("AuthMode = %q, want %q", got, tc.want)
			}
		})
	}
}

// An absent oauth key and an empty one mean different things, and JSON's null
// vs {} distinction is the only thing carrying that. Pin it.
func TestEmptyOAuthDiffersFromAbsent(t *testing.T) {
	absent := mustLoad(t, `{"servers":{"s":{"url":"https://e.example.com/mcp"}}}`)
	if absent.Servers["s"].OAuth != nil {
		t.Error("absent oauth decoded to a non-nil block")
	}
	empty := mustLoad(t, `{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{}}}}`)
	if empty.Servers["s"].OAuth == nil {
		t.Fatal("empty oauth decoded to nil — discovery would never be requested")
	}
}

func TestValidationErrors(t *testing.T) {
	cases := []struct {
		name, config, want string
	}{
		{"no servers", `{"servers":{}}`, "no servers configured"},
		{"missing url", `{"servers":{"s":{}}}`, "url is required"},
		{"bad scheme", `{"servers":{"s":{"url":"ftp://e.example.com"}}}`, "url must be http or https"},
		{"no host", `{"servers":{"s":{"url":"https:///mcp"}}}`, "has no host"},
		{
			"oauth and tokenCommand",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{},"tokenCommand":{"command":"x"}}}}`,
			"mutually exclusive",
		},
		{
			"tokenCommand without command",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","tokenCommand":{"args":["x"]}}}}`,
			"tokenCommand.command is required",
		},
		{
			"partial oauth",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"clientId":"id"}}}}`,
			"oauth.authorizeUrl, oauth.tokenUrl required",
		},
		{
			"callback port range",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id","callbackPort":70000}}}}`,
			"callbackPort must be between 1 and 65535",
		},
		{
			"callback scheme",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id","callbackScheme":"ftp"}}}}`,
			"callbackScheme must be",
		},
		{
			"client auth method",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id","clientAuthMethod":"jwt"}}}}`,
			"clientAuthMethod must be",
		},
		{
			"basic without secret",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id","clientAuthMethod":"basic"}}}}`,
			`"basic" requires oauth.clientSecret`,
		},
		{
			"none with secret",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"https://a.example.com/t","clientId":"id","clientSecret":"s","clientAuthMethod":"none"}}}}`,
			`"none" forbids oauth.clientSecret`,
		},
		{
			"http token endpoint",
			`{"servers":{"s":{"url":"https://e.example.com/mcp","oauth":{"authorizeUrl":"https://a.example.com/a","tokenUrl":"http://a.example.com/t","clientId":"id"}}}}`,
			"must be an https URL",
		},
		{
			"name with separator",
			`{"servers":{"a/b":{"url":"https://e.example.com/mcp"}}}`,
			"must not contain a path separator",
		},
		{"name is dotdot", `{"servers":{"..":{"url":"https://e.example.com/mcp"}}}`, "not a usable directory name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if msg := loadErr(t, tc.config); !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not contain %q", msg, tc.want)
			}
		})
	}
}

// Fixing a config one error per run is needless work when the file is open.
func TestAllProblemsReportedTogether(t *testing.T) {
	msg := loadErr(t, `{"servers":{"a":{},"b":{"url":"ftp://x.example.com"}}}`)
	if !strings.Contains(msg, "url is required") || !strings.Contains(msg, "must be http or https") {
		t.Errorf("only some problems reported: %s", msg)
	}
}

func TestServerLookupListsAlternatives(t *testing.T) {
	f := mustLoad(t, `{"servers":{"slack":{"url":"https://e.example.com/mcp"},"aws":{"url":"https://e.example.com/mcp"}}}`)
	_, err := f.Server("slak")
	if err == nil {
		t.Fatal("expected an error for an unknown server")
	}
	if !strings.Contains(err.Error(), "aws, slack") {
		t.Errorf("error does not list the configured servers: %v", err)
	}
}

func TestMissingFileError(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err == nil || !strings.Contains(err.Error(), "read config") {
		t.Errorf("unhelpful error for a missing file: %v", err)
	}
}

func TestDefaults(t *testing.T) {
	o := &OAuth{}
	if got := o.CallbackSchemeOrDefault(); got != "http" {
		t.Errorf("default callback scheme = %q", got)
	}
	if got := o.ClientAuthMethodOrDefault(); got != "post" {
		t.Errorf("default client auth method = %q", got)
	}
}

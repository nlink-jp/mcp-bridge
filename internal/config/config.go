// Package config loads and validates mcp-bridge's single configuration file.
//
// There is exactly one file and one shape. mcp-bridge's predecessor split
// configuration across a system-global file and per-server profiles, decoded
// one strictly and the other loosely, and read some keys only at login time
// and others only at run time. Nothing here is allowed to grow a second tier:
// a reader of config.json can see every setting that applies.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File is the whole configuration file.
type File struct {
	Servers map[string]*Server `json:"servers"`
}

// Server describes one upstream MCP server.
//
// Authentication is selected by which field is present, not by a mode string:
// no auth field at all means no authentication, Headers means static headers,
// TokenCommand means an external command supplies a Bearer token, and OAuth
// means the authorization_code flow. An OAuth block that is present but empty
// (`"oauth": {}`) asks for endpoint and client discovery.
type Server struct {
	URL          string            `json:"url"`
	Headers      map[string]string `json:"headers,omitempty"`
	OAuth        *OAuth            `json:"oauth,omitempty"`
	TokenCommand *TokenCommand     `json:"tokenCommand,omitempty"`
}

// OAuth holds the authorization_code settings for a pre-registered client.
//
// Providers that implement RFC 7591 Dynamic Client Registration need none of
// these: leave the block empty and mcp-bridge discovers the endpoints and
// registers a client. The fields exist for the providers that do not — Slack,
// GitHub Apps, Microsoft Entra ID — where an OAuth app is registered by hand
// and its redirect URI and token-endpoint authentication are fixed at
// registration time.
type OAuth struct {
	AuthorizeURL     string            `json:"authorizeUrl,omitempty"`
	TokenURL         string            `json:"tokenUrl,omitempty"`
	ClientID         string            `json:"clientId,omitempty"`
	ClientSecret     string            `json:"clientSecret,omitempty"`
	Scopes           []string          `json:"scopes,omitempty"`
	ExtraParams      map[string]string `json:"extraParams,omitempty"`
	CallbackPort     int               `json:"callbackPort,omitempty"`
	CallbackScheme   string            `json:"callbackScheme,omitempty"`
	ClientAuthMethod string            `json:"clientAuthMethod,omitempty"`
}

// TokenCommand runs an external command whose stdout is a Bearer token.
type TokenCommand struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// AuthMode names how a server authenticates.
type AuthMode string

const (
	AuthNone            AuthMode = "none"
	AuthStaticHeaders   AuthMode = "static-headers"
	AuthTokenCommand    AuthMode = "token-command"
	AuthOAuthConfigured AuthMode = "oauth"
	AuthOAuthDiscover   AuthMode = "oauth-discover"
)

// AuthMode reports how this server authenticates. Validate guarantees the
// combinations are unambiguous before this is called.
func (s *Server) AuthMode() AuthMode {
	switch {
	case s.OAuth != nil && s.OAuth.isEmpty():
		return AuthOAuthDiscover
	case s.OAuth != nil:
		return AuthOAuthConfigured
	case s.TokenCommand != nil:
		return AuthTokenCommand
	case len(s.Headers) > 0:
		return AuthStaticHeaders
	default:
		return AuthNone
	}
}

// isEmpty reports whether the block carries no configuration at all, which is
// the request for discovery.
func (o *OAuth) isEmpty() bool {
	return o.AuthorizeURL == "" && o.TokenURL == "" && o.ClientID == "" &&
		o.ClientSecret == "" && len(o.Scopes) == 0 && len(o.ExtraParams) == 0 &&
		o.CallbackPort == 0 && o.CallbackScheme == "" && o.ClientAuthMethod == ""
}

// CallbackSchemeOrDefault returns the loopback scheme to use for the OAuth
// callback listener.
func (o *OAuth) CallbackSchemeOrDefault() string {
	if o.CallbackScheme == "" {
		return "http"
	}
	return o.CallbackScheme
}

// ClientAuthMethodOrDefault returns how client credentials reach the token
// endpoint (RFC 6749 §2.3.1).
func (o *OAuth) ClientAuthMethodOrDefault() string {
	if o.ClientAuthMethod == "" {
		return "post"
	}
	return o.ClientAuthMethod
}

// Load reads and validates the configuration file at path.
//
// Decoding is strict: an unknown key is an error, not a shrug. Configuration
// is hand-written, and a silently ignored "callbackSchema" (for the correct
// "callbackScheme") turns into a feature that simply never happens, with
// nothing on screen to explain why.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := f.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

// Server returns the named server, or an error naming what is configured.
// The error lists the alternatives because the common failure is a typo in a
// name the MCP client passes, where the fix is obvious once the list is shown.
func (f *File) Server(name string) (*Server, error) {
	if s, ok := f.Servers[name]; ok {
		return s, nil
	}
	known := f.Names()
	if len(known) == 0 {
		return nil, fmt.Errorf("no server %q: the config defines no servers", name)
	}
	return nil, fmt.Errorf("no server %q: configured servers are %s", name, strings.Join(known, ", "))
}

// Names returns the configured server names in sorted order.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Servers))
	for name := range f.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate checks every server. All problems are reported together: fixing a
// config file one error per run is needless work when the file is right there.
func (f *File) Validate() error {
	if len(f.Servers) == 0 {
		return fmt.Errorf("no servers configured")
	}
	var problems []string
	for _, name := range f.Names() {
		if err := validateName(name); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		srv := f.Servers[name]
		if srv == nil {
			problems = append(problems, fmt.Sprintf("server %q: empty definition", name))
			continue
		}
		for _, err := range srv.problems(name) {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// validateName rejects names that cannot safely become a directory. Server
// names are used as the last path element of the state directory, so a name
// containing a separator or "." would place token files outside it.
func validateName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("server name must not be empty")
	case name == "." || name == "..":
		return fmt.Errorf("server name %q is not a usable directory name", name)
	case strings.ContainsRune(name, '/'), strings.ContainsRune(name, filepath.Separator):
		return fmt.Errorf("server name %q must not contain a path separator", name)
	case strings.ContainsRune(name, 0):
		return fmt.Errorf("server name contains a null byte")
	}
	return nil
}

// problems returns every validation failure for one server.
func (s *Server) problems(name string) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q: "+format, append([]any{name}, args...)...))
	}

	switch {
	case s.URL == "":
		add("url is required")
	default:
		u, err := url.Parse(s.URL)
		switch {
		case err != nil:
			add("url %q is not a valid URL: %v", s.URL, err)
		case u.Scheme != "http" && u.Scheme != "https":
			add("url must be http or https, got %q", u.Scheme)
		case u.Host == "":
			add("url %q has no host", s.URL)
		}
	}

	if s.OAuth != nil && s.TokenCommand != nil {
		add("oauth and tokenCommand are mutually exclusive")
	}
	if s.TokenCommand != nil && s.TokenCommand.Command == "" {
		add("tokenCommand.command is required")
	}
	for header := range s.Headers {
		if header == "" {
			add("headers contains an empty header name")
		}
	}

	if s.OAuth != nil && !s.OAuth.isEmpty() {
		errs = append(errs, s.OAuth.problems(name)...)
	}
	return errs
}

// problems validates a populated OAuth block. An empty block means discovery
// and never reaches here.
func (o *OAuth) problems(name string) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("server %q: "+format, append([]any{name}, args...)...))
	}

	// A partially filled block is the dangerous case: enough to look configured,
	// not enough to work. Name every missing field at once.
	var missing []string
	if o.AuthorizeURL == "" {
		missing = append(missing, "oauth.authorizeUrl")
	}
	if o.TokenURL == "" {
		missing = append(missing, "oauth.tokenUrl")
	}
	if o.ClientID == "" {
		missing = append(missing, "oauth.clientId")
	}
	if len(missing) > 0 {
		add("%s required for a pre-registered OAuth client (use \"oauth\": {} to discover them instead)",
			strings.Join(missing, ", "))
	}

	for _, field := range []struct {
		name, value string
	}{
		{"oauth.authorizeUrl", o.AuthorizeURL},
		{"oauth.tokenUrl", o.TokenURL},
	} {
		if field.value == "" {
			continue
		}
		u, err := url.Parse(field.value)
		if err != nil || u.Scheme != "https" {
			add("%s must be an https URL, got %q", field.name, field.value)
		}
	}

	if o.CallbackPort != 0 && (o.CallbackPort < 1 || o.CallbackPort > 65535) {
		add("oauth.callbackPort must be between 1 and 65535, got %d", o.CallbackPort)
	}

	switch o.CallbackScheme {
	case "", "http", "https":
	default:
		add("oauth.callbackScheme must be \"http\" or \"https\", got %q", o.CallbackScheme)
	}

	switch o.ClientAuthMethod {
	case "", "post", "basic", "none":
	default:
		add("oauth.clientAuthMethod must be \"post\", \"basic\", or \"none\", got %q", o.ClientAuthMethod)
	}
	if o.ClientAuthMethod == "basic" && o.ClientSecret == "" {
		add("oauth.clientAuthMethod \"basic\" requires oauth.clientSecret")
	}
	if o.ClientAuthMethod == "none" && o.ClientSecret != "" {
		add("oauth.clientAuthMethod \"none\" forbids oauth.clientSecret (use \"post\" or \"basic\" for a confidential client)")
	}

	return errs
}

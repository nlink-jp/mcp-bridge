package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// maxMetadataBytes bounds every discovery document read.
const maxMetadataBytes = 1 << 20

// Discovered is what discovery produces, and what the cache holds.
//
// RedirectURI is stored alongside the client because a dynamically registered
// client is bound to the redirect URI it was registered with. Caching it is
// what lets a fixed-port setup reuse one registration instead of creating a
// new client on the provider with every login.
type Discovered struct {
	AuthorizeURL string   `json:"authorize_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
	RedirectURI  string   `json:"redirect_uri,omitempty"`
}

// merge overlays explicitly configured settings on discovered ones. What the
// user wrote always wins.
func (d *Discovered) merge(s Settings) Settings {
	out := s
	if out.AuthorizeURL == "" {
		out.AuthorizeURL = d.AuthorizeURL
	}
	if out.TokenURL == "" {
		out.TokenURL = d.TokenURL
	}
	if out.ClientID == "" {
		out.ClientID = d.ClientID
	}
	if out.ClientSecret == "" {
		out.ClientSecret = d.ClientSecret
	}
	if len(out.Scopes) == 0 {
		out.Scopes = d.Scopes
	}
	// A dynamically registered client is registered as public; sending a
	// secret it was never given would be rejected.
	if out.ClientAuthMethod == "" && out.ClientSecret == "" {
		out.ClientAuthMethod = "none"
	}
	return out
}

type discoveryRequest struct {
	serverURL   string
	redirectURI string
	cachePath   string
	client      *http.Client
	out         io.Writer
}

// discover finds a server's OAuth endpoints and obtains a client for them.
//
// A cached registration is reused when it was made for the same redirect URI.
// Registering afresh every time works, but it litters the provider with
// single-use client records — visible and confusing in an admin console.
func discover(req discoveryRequest) (*Discovered, error) {
	if cached, ok := loadDiscovery(req.cachePath); ok && cached.RedirectURI == req.redirectURI && cached.ClientID != "" {
		fmt.Fprintf(req.out, "Reusing the client registered earlier for %s.\n", req.redirectURI)
		return cached, nil
	}

	if req.serverURL == "" {
		return nil, fmt.Errorf("cannot discover OAuth settings without the server URL")
	}
	fmt.Fprintln(req.out, "Discovering the authorization server...")

	meta, err := findAuthServer(req)
	if err != nil {
		return nil, err
	}

	clientID, clientSecret, err := registerClient(req, meta)
	if err != nil {
		return nil, err
	}

	discovered := &Discovered{
		AuthorizeURL: meta.AuthorizationEndpoint,
		TokenURL:     meta.TokenEndpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       meta.ScopesSupported,
		RedirectURI:  req.redirectURI,
	}

	// A cache that cannot be written costs a re-registration next time, not
	// this login.
	if err := saveDiscovery(req.cachePath, discovered); err != nil {
		fmt.Fprintf(req.out, "Warning: the discovered settings could not be cached: %v\n", err)
	}

	fmt.Fprintf(req.out, "Authorization server found: %s\n", meta.AuthorizationEndpoint)
	return discovered, nil
}

// authServerMetadata is OAuth 2.0 Authorization Server Metadata (RFC 8414),
// which OpenID Provider Metadata is a superset of.
type authServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
}

// findAuthServer locates the authorization server, in the order the MCP
// specification prescribes and then by the conventional well-known paths.
//
// The specified route is: an unauthenticated request draws a 401 whose
// WWW-Authenticate header points at the protected-resource metadata
// (RFC 9728), which names the authorization server. Servers that skip that
// still usually publish metadata at a well-known path on their own host, so
// both are tried before giving up — and the error, when it comes, says the
// manual configuration is the way forward rather than just reporting the last
// HTTP failure.
func findAuthServer(req discoveryRequest) (*authServerMetadata, error) {
	var attempts []string

	if resourceMetadataURL, err := probeForResourceMetadata(req); err == nil {
		if authServerURL, err := fetchResourceMetadata(req, resourceMetadataURL); err == nil {
			if meta, err := fetchAuthServerMetadata(req, authServerURL); err == nil {
				return meta, nil
			} else {
				attempts = append(attempts, fmt.Sprintf("authorization server metadata at %s: %v", authServerURL, err))
			}
		} else {
			attempts = append(attempts, fmt.Sprintf("protected resource metadata at %s: %v", resourceMetadataURL, err))
		}
	} else {
		attempts = append(attempts, fmt.Sprintf("challenge from %s: %v", req.serverURL, err))
	}

	origin, err := originOf(req.serverURL)
	if err != nil {
		return nil, err
	}
	if meta, err := fetchAuthServerMetadata(req, origin); err == nil {
		return meta, nil
	} else {
		attempts = append(attempts, fmt.Sprintf("well-known metadata on %s: %v", origin, err))
	}

	return nil, fmt.Errorf("could not discover the authorization server. "+
		"If this provider does not publish OAuth metadata, register an OAuth app with it and configure "+
		"oauth.authorizeUrl, oauth.tokenUrl, oauth.clientId and oauth.clientSecret explicitly.\nTried:\n  - %s",
		strings.Join(attempts, "\n  - "))
}

// probeForResourceMetadata makes an unauthenticated request and reads the
// resource_metadata pointer out of the 401 challenge.
func probeForResourceMetadata(req discoveryRequest) (string, error) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"discovery","method":"initialize","params":{}}`)
	httpReq, err := http.NewRequest(http.MethodPost, req.serverURL, body)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := req.client.Do(httpReq)
	if err != nil {
		return "", err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxMetadataBytes))
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("expected an HTTP 401 challenge, got %d — the server may not require authorization", resp.StatusCode)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	metadataURL := quotedParam(challenge, "resource_metadata")
	if metadataURL == "" {
		return "", fmt.Errorf("the challenge names no resource_metadata: %s", orDash(challenge))
	}
	return metadataURL, nil
}

// fetchResourceMetadata reads RFC 9728 metadata and returns the first
// authorization server it names.
func fetchResourceMetadata(req discoveryRequest, metadataURL string) (string, error) {
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := getJSON(req, metadataURL, &meta); err != nil {
		return "", err
	}
	if len(meta.AuthorizationServers) == 0 {
		return "", fmt.Errorf("no authorization_servers listed")
	}
	return meta.AuthorizationServers[0], nil
}

// fetchAuthServerMetadata tries the well-known metadata paths for an issuer.
//
// Both are tried because providers publish one or the other: RFC 8414 defines
// the OAuth path, while identity providers built around OpenID Connect
// frequently publish only the OpenID one.
func fetchAuthServerMetadata(req discoveryRequest, issuer string) (*authServerMetadata, error) {
	parsed, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse issuer %q: %w", issuer, err)
	}
	path := strings.TrimSuffix(parsed.Path, "/")

	var problems []string
	for _, wellKnown := range []string{"/.well-known/oauth-authorization-server", "/.well-known/openid-configuration"} {
		// RFC 8414 inserts the well-known segment before the issuer path
		// rather than appending it.
		candidate := fmt.Sprintf("%s://%s%s%s", parsed.Scheme, parsed.Host, wellKnown, path)
		var meta authServerMetadata
		if err := getJSON(req, candidate, &meta); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			problems = append(problems, fmt.Sprintf("%s: metadata names no authorization or token endpoint", candidate))
			continue
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
}

// registerClient performs RFC 7591 dynamic client registration.
func registerClient(req discoveryRequest, meta *authServerMetadata) (clientID, clientSecret string, err error) {
	if meta.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("%s does not support dynamic client registration. "+
			"Register an OAuth app with the provider and configure oauth.authorizeUrl, oauth.tokenUrl, "+
			"oauth.clientId and oauth.clientSecret explicitly — that is what mcp-bridge is for",
			orDash(meta.Issuer))
	}

	fmt.Fprintln(req.out, "Registering a client with the authorization server...")
	payload, err := json.Marshal(map[string]any{
		"client_name":                "mcp-bridge",
		"redirect_uris":              []string{req.redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none", // public client; PKCE is the protection
	})
	if err != nil {
		return "", "", err
	}

	resp, err := req.client.Post(meta.RegistrationEndpoint, "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return "", "", fmt.Errorf("register a client at %s: %w", meta.RegistrationEndpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return "", "", err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", "", fmt.Errorf("client registration was refused (HTTP %d): %s. "+
			"Some servers advertise a registration endpoint but reject registrations in practice; "+
			"if so, register an OAuth app by hand and configure the oauth block explicitly",
			resp.StatusCode, describeOAuthError(body))
	}

	var registered struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(body, &registered); err != nil {
		return "", "", fmt.Errorf("parse the registration response: %w", err)
	}
	if registered.ClientID == "" {
		return "", "", fmt.Errorf("the registration response contained no client_id")
	}
	return registered.ClientID, registered.ClientSecret, nil
}

// getJSON fetches and decodes one discovery document.
func getJSON(req discoveryRequest, rawURL string, into any) error {
	resp, err := req.client.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("not valid JSON metadata")
	}
	return nil
}

// originOf reduces a URL to scheme://host.
func originOf(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", rawURL, err)
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), nil
}

// quotedParam pulls a quoted value out of a WWW-Authenticate challenge, e.g.
// resource_metadata="https://example.com/.well-known/...".
func quotedParam(header, name string) string {
	key := name + `="`
	start := strings.Index(header, key)
	if start < 0 {
		return ""
	}
	start += len(key)
	end := strings.Index(header[start:], `"`)
	if end < 0 {
		return ""
	}
	return header[start : start+end]
}

// loadDiscovery reads the cache. A missing or unreadable cache is simply an
// absent one: discovery runs again.
func loadDiscovery(path string) (*Discovered, bool) {
	if path == "" {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var d Discovered
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, false
	}
	return &d, true
}

// saveDiscovery caches the result. It may hold a client secret, so it is
// written with the same care as the token file.
func saveDiscovery(path string, d *Discovered) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

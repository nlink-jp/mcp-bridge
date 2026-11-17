package oauth

import (
	"encoding/json"
	"errors"
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

// allowHTTP reports whether discovery may use http URLs: only when the
// configured server is itself http. Whoever wrote that URL chose an
// unencrypted server, and its authorization server may be the same kind. An
// https server gets no such latitude, or one response from it could move the
// authorization code and the tokens onto a connection anyone on the path can
// read (ADR-0006).
func (r discoveryRequest) allowHTTP() bool {
	u, err := url.Parse(r.serverURL)
	return err == nil && u.Scheme == "http"
}

// checkDiscoveredURL decides whether a URL that discovery was handed — by the
// server's challenge or by a metadata document — may be used.
func checkDiscoveredURL(rawURL string, allowHTTP bool) error {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%q is not an absolute URL", rawURL)
	}
	switch {
	case u.Scheme == "https":
		return nil
	case u.Scheme == "http" && allowHTTP:
		return nil
	case u.Scheme == "http":
		return fmt.Errorf("%s is not https, and only a server configured with an http url may name an http one", rawURL)
	default:
		return fmt.Errorf("%s is neither https nor http", rawURL)
	}
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

	found, err := findAuthServer(req)
	if err != nil {
		return nil, err
	}
	meta := found.meta

	clientID, clientSecret, err := registerClient(req, meta)
	if err != nil {
		return nil, err
	}

	scopes, _, _ := chooseScopes(found.challengeScopes, found.resourceScopes, meta.ScopesSupported)
	discovered := &Discovered{
		AuthorizeURL: meta.AuthorizationEndpoint,
		TokenURL:     meta.TokenEndpoint,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       scopes,
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

// located is what finding the authorization server yields: its metadata, and
// the scopes the MCP server itself asked for on the way there.
type located struct {
	meta *authServerMetadata
	// namedBy is the protected-resource metadata document that named the
	// authorization server; empty when it was found on the server's own host.
	namedBy         string
	challengeScopes []string
	resourceScopes  []string
}

// findAuthServer locates the authorization server in the order the MCP
// specification (2025-11-25) prescribes, and then by the older convention.
//
// The specified route: an unauthenticated request draws a 401 whose
// WWW-Authenticate header points at the server's protected-resource metadata
// (RFC 9728); a server that does not say where it is publishes it at a
// well-known location; that document names the authorization server. Servers
// that predate this publish authorization server metadata on their own host,
// so that is tried last — and the error, when it comes, lists every attempt
// rather than just the last HTTP failure.
func findAuthServer(req discoveryRequest) (*located, error) {
	var attempts []string

	probe, err := probeChallenge(req)
	if err != nil {
		attempts = append(attempts, fmt.Sprintf("challenge from %s: %v", req.serverURL, err))
	}

	candidates := []string{probe.metadataURL}
	if probe.metadataURL == "" {
		if candidates, err = resourceMetadataURLs(req.serverURL); err != nil {
			return nil, err
		}
	}

	for _, candidate := range candidates {
		resource, err := fetchResourceMetadata(req, candidate)
		if mismatch := (*resourceMismatchError)(nil); errors.As(err, &mismatch) {
			// Metadata that describes some other resource is not a missing
			// document to look past: the server is publishing something that
			// disagrees with the URL it was reached at.
			return nil, err
		}
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("protected resource metadata at %s: %v", candidate, err))
			continue
		}
		issuer := resource.AuthorizationServers[0]
		meta, err := fetchAuthServerMetadata(req, issuer)
		if err != nil {
			attempts = append(attempts, fmt.Sprintf("authorization server metadata for %s (named by %s): %v", issuer, candidate, err))
			break
		}
		return &located{
			meta:            meta,
			namedBy:         candidate,
			challengeScopes: probe.scopes,
			resourceScopes:  resource.ScopesSupported,
		}, nil
	}

	origin, err := originOf(req.serverURL)
	if err != nil {
		return nil, err
	}
	meta, err := fetchAuthServerMetadata(req, origin)
	if err != nil {
		attempts = append(attempts, fmt.Sprintf("well-known metadata on %s: %v", origin, err))
		return nil, fmt.Errorf("could not discover the authorization server. "+
			"If this provider does not publish OAuth metadata, register an OAuth app with it and configure "+
			"oauth.authorizeUrl, oauth.tokenUrl, oauth.clientId and oauth.clientSecret explicitly.\nTried:\n  - %s",
			strings.Join(attempts, "\n  - "))
	}
	return &located{meta: meta, challengeScopes: probe.scopes}, nil
}

// probed is what the server's answer to an unauthenticated request said.
type probed struct {
	metadataURL string
	scopes      []string
}

// probeChallenge makes an unauthenticated request and reads the 401 challenge:
// where the server's metadata is, and which scopes it asks for.
//
// An error here is one more attempt to report, not the end of discovery: a
// server that answers without a challenge, or does not answer this request at
// all, may still publish its metadata at a well-known location.
func probeChallenge(req discoveryRequest) (probed, error) {
	body := strings.NewReader(`{"jsonrpc":"2.0","id":"discovery","method":"initialize","params":{}}`)
	httpReq, err := http.NewRequest(http.MethodPost, req.serverURL, body)
	if err != nil {
		return probed{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := req.client.Do(httpReq)
	if err != nil {
		return probed{}, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxMetadataBytes))
	resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		return probed{}, fmt.Errorf("expected an HTTP 401 challenge, got %d — the server may not require authorization", resp.StatusCode)
	}
	challenges := resp.Header.Values("WWW-Authenticate")
	out := probed{
		metadataURL: bearerParam(challenges, "resource_metadata"),
		scopes:      strings.Fields(bearerParam(challenges, "scope")),
	}
	if out.metadataURL == "" {
		return out, fmt.Errorf("the challenge names no resource_metadata: %s", orDash(strings.Join(challenges, ", ")))
	}
	return out, nil
}

// resourceMetadata is OAuth 2.0 Protected Resource Metadata (RFC 9728), as far
// as discovery reads it.
type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// resourceMismatchError reports metadata that describes a resource other than
// the server it was fetched for.
type resourceMismatchError struct {
	metadataURL, resource, serverURL string
}

func (e *resourceMismatchError) Error() string {
	return fmt.Sprintf("the metadata at %s describes %s, which is not this server (%s). "+
		"What the server publishes disagrees with the configured url; check the url, "+
		"or configure oauth.authorizeUrl and oauth.tokenUrl explicitly if the server's metadata is wrong",
		e.metadataURL, e.resource, e.serverURL)
}

// fetchResourceMetadata reads one RFC 9728 document and accepts it only if it
// is this server's own and names an authorization server.
//
// The resource check is what stops a server from handing over another
// resource's metadata, so that the login asks that resource's authorization
// server for a token and then presents it here (RFC 9728 §3.3).
func fetchResourceMetadata(req discoveryRequest, metadataURL string) (*resourceMetadata, error) {
	var meta resourceMetadata
	if err := getJSON(req, metadataURL, &meta); err != nil {
		return nil, err
	}
	if meta.Resource == "" {
		return nil, fmt.Errorf("the document names no resource, so it cannot be tied to this server")
	}
	if !resourceMatches(meta.Resource, req.serverURL) {
		return nil, &resourceMismatchError{metadataURL: metadataURL, resource: meta.Resource, serverURL: req.serverURL}
	}
	if len(meta.AuthorizationServers) == 0 {
		return nil, fmt.Errorf("no authorization_servers listed")
	}
	return &meta, nil
}

// resourceMetadataURLs lists the well-known locations of a server's
// protected-resource metadata: with the server's path, then at the root
// (MCP specification 2025-11-25; RFC 9728 §3.1 for how the first is built —
// the well-known segment goes between the host and the path, and a query
// stays).
func resourceMetadataURLs(serverURL string) ([]string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", serverURL, err)
	}
	const wellKnown = "/.well-known/oauth-protected-resource"
	origin := u.Scheme + "://" + u.Host

	path := u.EscapedPath()
	if path == "/" {
		path = ""
	}
	suffix := path
	if u.RawQuery != "" {
		suffix += "?" + u.RawQuery
	}

	root := origin + wellKnown
	if suffix == "" {
		return []string{root}, nil
	}
	return []string{root + suffix, root}, nil
}

// resourceMatches reports whether a metadata document's resource identifier is
// the server at serverURL: the same scheme, host and port, and a path that is
// a leading part of the server's, by whole segments.
//
// RFC 9728 §3.3 asks for the two to be identical. This is the looser rule the
// official MCP SDK applies, chosen so that mcp-bridge does not refuse a server
// those clients connect to (ADR-0006).
func resourceMatches(resource, serverURL string) bool {
	r, err := url.Parse(resource)
	if err != nil || r.Host == "" || r.Fragment != "" {
		return false
	}
	s, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	if r.Scheme != s.Scheme || !strings.EqualFold(r.Hostname(), s.Hostname()) || portOf(r) != portOf(s) {
		return false
	}
	resourcePath := strings.TrimSuffix(r.Path, "/")
	serverPath := strings.TrimSuffix(s.Path, "/")
	return resourcePath == serverPath || strings.HasPrefix(serverPath, resourcePath+"/")
}

// portOf returns a URL's port, with the scheme's default when none is written.
func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch u.Scheme {
	case "https":
		return "443"
	case "http":
		return "80"
	}
	return ""
}

// Where a login's scopes came from, in the words the login prints.
const (
	scopesFromChallenge  = "the server's 401 response"
	scopesFromResource   = "the server's metadata"
	scopesFromAuthServer = "the authorization server's metadata"
)

// chooseScopes picks the scopes to request when the configuration names none.
//
// The order is the MCP specification's (2025-11-25, "Scope Selection
// Strategy"): what the server's challenge asks for, then what its metadata
// lists. The authorization server's own list comes last and is not in the
// specification; it is what discovery used before, and `"oauth": {}` has no
// way to write scopes if it stopped working.
//
// A refresh token is not something a resource asks for, so servers leave
// offline_access out of both of the first two, and an OpenID provider such as
// Microsoft Entra ID issues no refresh token without it. It is therefore added
// when the authorization server lists it — the next revision of the
// specification says a client may — and reported as added.
func chooseScopes(challenge, resource, authServer []string) (scopes []string, source string, addedOfflineAccess bool) {
	switch {
	case len(challenge) > 0:
		scopes, source = challenge, scopesFromChallenge
	case len(resource) > 0:
		scopes, source = resource, scopesFromResource
	case len(authServer) > 0:
		return authServer, scopesFromAuthServer, false
	default:
		return nil, "", false
	}
	const offline = "offline_access"
	if contains(authServer, offline) && !contains(scopes, offline) {
		scopes = append(append([]string{}, scopes...), offline)
		addedOfflineAccess = true
	}
	return scopes, source, addedOfflineAccess
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// fetchAuthServerMetadata tries the well-known metadata locations for an
// issuer and returns the first document that names both endpoints.
func fetchAuthServerMetadata(req discoveryRequest, issuer string) (*authServerMetadata, error) {
	candidates, err := authServerMetadataURLs(issuer)
	if err != nil {
		return nil, err
	}

	var problems []string
	for _, candidate := range candidates {
		var meta authServerMetadata
		if err := getJSON(req, candidate, &meta); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
			problems = append(problems, fmt.Sprintf("%s: metadata names no authorization or token endpoint", candidate))
			continue
		}
		if err := firstError(
			checkDiscoveredURL(meta.AuthorizationEndpoint, req.allowHTTP()),
			checkDiscoveredURL(meta.TokenEndpoint, req.allowHTTP()),
		); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", candidate, err))
			continue
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("%s", strings.Join(problems, "; "))
}

// authServerMetadataURLs lists where an issuer's metadata may be, in the order
// the MCP specification (2025-11-25, "Authorization Server Metadata
// Discovery") prescribes.
//
// RFC 8414 inserts the well-known segment before the issuer's path, and OpenID
// Connect Discovery appends it after. Providers publish at one or another, so
// all are tried: Microsoft Entra ID serves only the appended form, and answers
// 404 at both inserted ones. For an issuer without a path the appended form is
// the same URL as the inserted one, so it is not repeated.
func authServerMetadataURLs(issuer string) ([]string, error) {
	parsed, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse issuer %q: %w", issuer, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("issuer %q is not an absolute URL", issuer)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	path := strings.TrimSuffix(parsed.Path, "/")

	urls := []string{
		origin + "/.well-known/oauth-authorization-server" + path,
		origin + "/.well-known/openid-configuration" + path,
	}
	if path != "" {
		urls = append(urls, origin+path+"/.well-known/openid-configuration")
	}
	return urls, nil
}

// registerClient performs RFC 7591 dynamic client registration.
func registerClient(req discoveryRequest, meta *authServerMetadata) (clientID, clientSecret string, err error) {
	if meta.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("%s does not support dynamic client registration. "+
			"Register an OAuth app with the provider and configure oauth.authorizeUrl, oauth.tokenUrl, "+
			"oauth.clientId and oauth.clientSecret explicitly — that is what mcp-bridge is for",
			orDash(meta.Issuer))
	}

	if err := checkDiscoveredURL(meta.RegistrationEndpoint, req.allowHTTP()); err != nil {
		return "", "", fmt.Errorf("the registration endpoint cannot be used: %w", err)
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
	if err := checkDiscoveredURL(rawURL, req.allowHTTP()); err != nil {
		return err
	}
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

// firstError returns the first non-nil error.
func firstError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
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

// LoadDiscovered reads the discovery cache. A missing or unreadable cache is
// simply an absent one, and the second return value says so; discovery runs
// again in that case.
//
// A session started with `"oauth": {}` has no endpoints in its config, so this
// is where it finds the ones the login recorded.
func LoadDiscovered(path string) (*Discovered, bool) { return loadDiscovery(path) }

// loadDiscovery reads the cache.
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

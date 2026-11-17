package oauth

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// The order is the MCP specification's (2025-11-25, "Authorization Server
// Metadata Discovery"), and the expected strings below are its own examples
// with the issuer substituted.
func TestAuthServerMetadataLocations(t *testing.T) {
	cases := []struct {
		issuer string
		want   []string
	}{
		{
			"https://auth.example.com/tenant1",
			[]string{
				"https://auth.example.com/.well-known/oauth-authorization-server/tenant1",
				"https://auth.example.com/.well-known/openid-configuration/tenant1",
				"https://auth.example.com/tenant1/.well-known/openid-configuration",
			},
		},
		{
			"https://auth.example.com",
			[]string{
				"https://auth.example.com/.well-known/oauth-authorization-server",
				"https://auth.example.com/.well-known/openid-configuration",
			},
		},
		{
			// A trailing slash is not a path of its own.
			"https://auth.example.com/",
			[]string{
				"https://auth.example.com/.well-known/oauth-authorization-server",
				"https://auth.example.com/.well-known/openid-configuration",
			},
		},
		{
			// The shape of a Microsoft Entra ID issuer.
			"https://login.example.com/tenant-id/v2.0",
			[]string{
				"https://login.example.com/.well-known/oauth-authorization-server/tenant-id/v2.0",
				"https://login.example.com/.well-known/openid-configuration/tenant-id/v2.0",
				"https://login.example.com/tenant-id/v2.0/.well-known/openid-configuration",
			},
		},
	}
	for _, tc := range cases {
		got, err := authServerMetadataURLs(tc.issuer)
		if err != nil {
			t.Errorf("%s: %v", tc.issuer, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s:\n got %v\nwant %v", tc.issuer, got, tc.want)
		}
	}
}

func TestIssuerMustBeAnAbsoluteURL(t *testing.T) {
	for _, issuer := range []string{"", "auth.example.com", "/tenant1"} {
		if _, err := authServerMetadataURLs(issuer); err == nil {
			t.Errorf("issuer %q was accepted", issuer)
		}
	}
}

// entraMetadata is the authorization server metadata Microsoft Entra ID
// publishes, reduced to the fields that matter here. The values are the ones
// https://login.microsoftonline.com/common/v2.0/.well-known/openid-configuration
// returned on 2026-11-17: no registration_endpoint, four scopes, no "none"
// among the authentication methods, and no code_challenge_methods_supported.
func entraMetadata(base string) map[string]any {
	return map[string]any{
		"issuer":                 base + "/tenant-id/v2.0",
		"authorization_endpoint": base + "/authorize",
		"token_endpoint":         base + "/token",
		"scopes_supported":       []string{"openid", "profile", "email", "offline_access"},
		"token_endpoint_auth_methods_supported": []string{
			"client_secret_post", "private_key_jwt", "client_secret_basic", "self_signed_tls_client_auth",
		},
	}
}

// Entra ID answers 404 at both locations that insert the well-known segment
// and serves its document only where OpenID Connect Discovery puts it
// (measured 2026-11-17). Stopping after the first two never finds it.
func TestMetadataIsFoundAtTheAppendedLocation(t *testing.T) {
	var asked []string
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path != "/tenant-id/v2.0/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entraMetadata(srv.URL))
	}))
	defer srv.Close()

	req := discoveryRequest{serverURL: srv.URL + "/mcp", client: srv.Client(), out: &bytes.Buffer{}}
	meta, err := fetchAuthServerMetadata(req, srv.URL+"/tenant-id/v2.0")
	if err != nil {
		t.Fatalf("metadata not found; asked %v: %v", asked, err)
	}
	if meta.TokenEndpoint != srv.URL+"/token" {
		t.Errorf("token endpoint = %q", meta.TokenEndpoint)
	}
	want := []string{
		"/.well-known/oauth-authorization-server/tenant-id/v2.0",
		"/.well-known/openid-configuration/tenant-id/v2.0",
		"/tenant-id/v2.0/.well-known/openid-configuration",
	}
	if !reflect.DeepEqual(asked, want) {
		t.Errorf("locations asked, in order:\n got %v\nwant %v", asked, want)
	}
}

// When no location has a document, the error has to say where it looked.
func TestMetadataFailureListsEveryLocation(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	req := discoveryRequest{serverURL: srv.URL + "/mcp", client: srv.Client(), out: &bytes.Buffer{}}
	_, err := fetchAuthServerMetadata(req, srv.URL+"/tenant-id/v2.0")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"/.well-known/oauth-authorization-server/tenant-id/v2.0",
		"/.well-known/openid-configuration/tenant-id/v2.0",
		"/tenant-id/v2.0/.well-known/openid-configuration",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name %s: %v", want, err)
		}
	}
}

func TestDiscoveredURLCondition(t *testing.T) {
	cases := []struct {
		url       string
		allowHTTP bool
		ok        bool
	}{
		{"https://auth.example.com/token", false, true},
		{"https://auth.example.com/token", true, true},
		{"http://auth.example.com/token", true, true},
		{"http://auth.example.com/token", false, false},
		// Loopback is no exception: an https server must not be able to aim
		// requests at whatever listens on the user's own machine.
		{"http://127.0.0.1:8080/token", false, false},
		{"http://localhost/token", false, false},
		{"ftp://auth.example.com/token", true, false},
		{"/token", true, false},
		{"", true, false},
		{"https:///token", true, false},
	}
	for _, tc := range cases {
		err := checkDiscoveredURL(tc.url, tc.allowHTTP)
		if (err == nil) != tc.ok {
			t.Errorf("checkDiscoveredURL(%q, allowHTTP=%v) = %v, want ok=%v", tc.url, tc.allowHTTP, err, tc.ok)
		}
	}
}

// counting wraps a handler and counts the requests that reach it.
func counting(n *int, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*n++
		h.ServeHTTP(w, r)
	})
}

// An https server that names an http authorization server gets no request sent
// there at all.
func TestHTTPSServerCannotNameAnHTTPAuthorizationServer(t *testing.T) {
	var hits int
	plain := httptest.NewServer(counting(&hits, http.NotFoundHandler()))
	defer plain.Close()

	req := discoveryRequest{serverURL: "https://mcp.example.com/mcp", client: plain.Client(), out: &bytes.Buffer{}}
	_, err := fetchAuthServerMetadata(req, plain.URL)
	if err == nil || !strings.Contains(err.Error(), "is not https") {
		t.Fatalf("expected the http issuer to be refused, got %v", err)
	}
	if hits != 0 {
		t.Errorf("%d requests reached the http authorization server", hits)
	}
}

// The same holds one step later: https metadata that names an http token
// endpoint is not usable.
func TestHTTPSMetadataCannotNameAnHTTPEndpoint(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         "http://auth.example.com/token",
		})
	}))
	defer srv.Close()

	req := discoveryRequest{serverURL: srv.URL + "/mcp", client: srv.Client(), out: &bytes.Buffer{}}
	_, err := fetchAuthServerMetadata(req, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "http://auth.example.com/token is not https") {
		t.Fatalf("expected the http token endpoint to be refused, got %v", err)
	}
}

// downgradeFixture is an https server that redirects every request to a plain
// http one, and a client that trusts the former and applies the production
// redirect rule.
func downgradeFixture(t *testing.T, status int) (httpsURL string, client *http.Client, plainHits *int) {
	t.Helper()
	hits := new(int)
	plain := httptest.NewServer(counting(hits, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"leaked","authorization_endpoint":"https://x.example.com/a","token_endpoint":"https://x.example.com/t"}`))
	})))
	t.Cleanup(plain.Close)

	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+r.URL.Path, status)
	}))
	t.Cleanup(secure.Close)

	client = defaultHTTPClient()
	client.Transport = secure.Client().Transport
	return secure.URL, client, hits
}

func TestMetadataFetchDoesNotFollowARedirectOffHTTPS(t *testing.T) {
	httpsURL, client, plainHits := downgradeFixture(t, http.StatusFound)

	req := discoveryRequest{serverURL: httpsURL + "/mcp", client: client, out: &bytes.Buffer{}}
	var into map[string]any
	err := getJSON(req, httpsURL+"/.well-known/openid-configuration", &into)
	if err == nil || !strings.Contains(err.Error(), "refusing a redirect from https to http://") {
		t.Fatalf("expected the redirect to be refused, got %v", err)
	}
	if *plainHits != 0 {
		t.Errorf("%d requests followed the redirect to http", *plainHits)
	}
}

// The token request holds the authorization code and the PKCE verifier (or a
// refresh token and a client secret), so no form of redirect may carry it off
// https. A 302 would be followed as a bodiless GET and is refused by the rule;
// a 307 asks for the form again, which the request cannot replay, so Go hands
// the 307 back and the exchange fails on it. Either way nothing reaches http.
func TestTokenRequestDoesNotFollowARedirectOffHTTPS(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		httpsURL, client, plainHits := downgradeFixture(t, status)

		tokens, err := exchange(tokenRequest{
			client:   client,
			tokenURL: httpsURL + "/token",
			form:     map[string][]string{"grant_type": {"authorization_code"}, "code": {"the-code"}},
			clientID: "id",
		})
		if err == nil {
			t.Errorf("HTTP %d: the exchange succeeded with %+v", status, tokens)
		}
		if *plainHits != 0 {
			t.Errorf("HTTP %d: %d requests followed the redirect to http", status, *plainHits)
		}
		if status == http.StatusFound && !strings.Contains(err.Error(), "refusing a redirect from https to http://") {
			t.Errorf("HTTP %d: error does not name the refused redirect: %v", status, err)
		}
	}
}

// An http server stays on Go's default: its redirects are followed.
func TestHTTPRedirectsAreStillFollowed(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer target.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer hop.Close()

	req := discoveryRequest{serverURL: hop.URL + "/mcp", client: defaultHTTPClient(), out: &bytes.Buffer{}}
	var into map[string]any
	if err := getJSON(req, hop.URL+"/meta", &into); err != nil {
		t.Fatal(err)
	}
	if into["ok"] != true {
		t.Errorf("redirect was not followed: %v", into)
	}
}

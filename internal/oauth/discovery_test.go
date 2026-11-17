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

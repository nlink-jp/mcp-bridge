package oauth

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// publishes makes the fake provider an MCP server too: it answers the
// unauthenticated probe with the given challenge, and serves protected-resource
// metadata at the given path naming itself as the authorization server.
func (p *fakeProvider) publishes(metadataPath string, scopes []string, challenge ...string) {
	p.route("/mcp", challengeWith(challenge...))
	doc := map[string]any{
		"resource":              p.URL + "/mcp",
		"authorization_servers": []string{p.URL},
	}
	if scopes != nil {
		doc["scopes_supported"] = scopes
	}
	p.route(metadataPath, serveJSON(doc))
}

// The server's own metadata decides the scopes ahead of the authorization
// server's list, which is all discovery used to read.
func TestScopesComeFromTheServersMetadata(t *testing.T) {
	p := newFakeProvider(t)
	const meta = "/.well-known/oauth-protected-resource/mcp"
	p.publishes(meta, []string{"mail.read", "mail.send"}, `Bearer resource_metadata="`+p.URL+meta+`"`)

	if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "mail.read mail.send" {
		t.Errorf("scope = %q, want the server's metadata scopes", got)
	}
}

// And the challenge decides ahead of the metadata.
func TestScopesInTheChallengeWin(t *testing.T) {
	p := newFakeProvider(t)
	const meta = "/.well-known/oauth-protected-resource/mcp"
	p.publishes(meta, []string{"mail.read", "mail.send"},
		`Bearer resource_metadata="`+p.URL+meta+`", scope="mail.read"`)

	if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "mail.read" {
		t.Errorf("scope = %q, want the challenge's", got)
	}
}

// A server that publishes no scopes of its own still gets the authorization
// server's list, as before.
func TestScopesFallBackToTheAuthorizationServersList(t *testing.T) {
	p := newFakeProvider(t)
	const meta = "/.well-known/oauth-protected-resource/mcp"
	p.publishes(meta, nil, `Bearer resource_metadata="`+p.URL+meta+`"`)

	if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "read write" {
		t.Errorf("scope = %q, want the authorization server's list", got)
	}
}

// Without a pointer in the challenge, the well-known locations are tried: with
// the server's path first, then the root.
func TestMetadataIsFoundAtTheWellKnownLocations(t *testing.T) {
	cases := []struct {
		name, servedAt string
		challenge      http.HandlerFunc
		wantAsked      []string
	}{
		{
			"with the path, after a challenge that names nothing",
			"/.well-known/oauth-protected-resource/mcp",
			challengeWith(`Bearer realm="mcp"`),
			[]string{"/.well-known/oauth-protected-resource/mcp"},
		},
		{
			"at the root, after the path has nothing",
			"/.well-known/oauth-protected-resource",
			challengeWith(`Bearer realm="mcp"`),
			[]string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"},
		},
		{
			"when the server does not challenge at all",
			"/.well-known/oauth-protected-resource/mcp",
			http.NotFound,
			[]string{"/.well-known/oauth-protected-resource/mcp"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeProvider(t)
			p.publishes(tc.servedAt, []string{"from-metadata"})
			p.route("/mcp", tc.challenge)

			if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
				t.Fatal(err)
			}
			if got := p.authorizeReq.Get("scope"); got != "from-metadata" {
				t.Errorf("scope = %q: the metadata at %s was not used", got, tc.servedAt)
			}
			for _, path := range tc.wantAsked {
				if p.hitsOn(path) != 1 {
					t.Errorf("%s was asked %d times, want 1", path, p.hitsOn(path))
				}
			}
		})
	}
}

// When the challenge says where the metadata is, nowhere else is tried.
func TestTheChallengesPointerIsTheOnlyCandidate(t *testing.T) {
	p := newFakeProvider(t)
	p.publishes("/elsewhere", []string{"from-pointer"}, `Bearer resource_metadata="`+p.URL+`/elsewhere"`)

	if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "from-pointer" {
		t.Errorf("scope = %q", got)
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource"} {
		if n := p.hitsOn(path); n != 0 {
			t.Errorf("%s was asked %d times although the challenge named the document", path, n)
		}
	}
}

// Metadata that describes another resource stops the login: no browser, no
// registration, and an error that shows both sides of the disagreement.
func TestMetadataForAnotherResourceStopsTheLogin(t *testing.T) {
	p := newFakeProvider(t)
	const meta = "/.well-known/oauth-protected-resource/mcp"
	p.route("/mcp", challengeWith(`Bearer resource_metadata="`+p.URL+meta+`"`))
	p.route(meta, serveJSON(map[string]any{
		"resource":              "https://victim.example.com/api",
		"authorization_servers": []string{p.URL},
		"scopes_supported":      []string{"victim.read"},
	}))

	cfg := loginConfig(t, p, Settings{})
	opened := false
	cfg.OpenBrowser = func(string) error { opened = true; return nil }

	_, err := Login(cfg)
	if err == nil {
		t.Fatal("the login went ahead with another resource's metadata")
	}
	for _, want := range []string{"https://victim.example.com/api", p.URL + "/mcp", "disagrees with the configured url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q: %v", want, err)
		}
	}
	if opened {
		t.Error("the browser was opened")
	}
	if p.registrations != 0 {
		t.Errorf("%d clients were registered", p.registrations)
	}
}

// A document that names no resource cannot be tied to the server, so it is
// passed over — here to the root location, which has a proper one.
func TestMetadataWithoutAResourceIsPassedOver(t *testing.T) {
	p := newFakeProvider(t)
	p.publishes("/.well-known/oauth-protected-resource", []string{"from-root"})
	p.route("/mcp", challengeWith(`Bearer realm="mcp"`))
	p.route("/.well-known/oauth-protected-resource/mcp", serveJSON(map[string]any{
		"authorization_servers": []string{p.URL},
		"scopes_supported":      []string{"untied"},
	}))

	if _, err := Login(loginConfig(t, p, Settings{})); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "from-root" {
		t.Errorf("scope = %q, want the root document's", got)
	}
}

// When nothing at all is published, the error lists every place that was
// asked, so the reader can see what the server does not have.
func TestNothingPublishedListsEveryAttempt(t *testing.T) {
	p := newFakeProvider(t)
	p.route("/mcp", challengeWith(`Bearer realm="mcp"`))
	p.route("/.well-known/oauth-authorization-server", http.NotFound)

	cfg := loginConfig(t, p, Settings{})
	cfg.Out = &bytes.Buffer{}
	_, err := Login(cfg)
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"the challenge names no resource_metadata",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-protected-resource:",
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

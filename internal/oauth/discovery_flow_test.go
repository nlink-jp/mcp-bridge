package oauth

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"reflect"
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

// entraShaped makes the fake provider look, from discovery's side, like an MCP
// server protected by Microsoft Entra ID: a challenge that points at
// protected-resource metadata, an issuer with a path, authorization server
// metadata only at the appended location (measured 2026-11-17), the content of
// Entra ID's public metadata, and no registration endpoint anywhere.
func entraShaped(t *testing.T) *fakeProvider {
	t.Helper()
	p := newFakeProvider(t)
	p.route("/.well-known/oauth-authorization-server", http.NotFound)
	p.route("/register", http.NotFound)
	p.route("/mcp", challengeWith(`Bearer resource_metadata="`+p.URL+`/prm"`))
	p.route("/prm", serveJSON(map[string]any{
		"resource":              p.URL + "/mcp",
		"authorization_servers": []string{p.URL + "/tenant-id/v2.0"},
		"scopes_supported":      []string{"api://mcp-mail/.default"},
	}))
	p.route("/tenant-id/v2.0/.well-known/openid-configuration", serveJSON(entraMetadata(p.URL)))
	return p
}

// The configuration the report asked for: a pre-registered public client, the
// localhost redirect URI, and nothing else written.
func entraSettings(port int) Settings {
	return Settings{
		ClientID:         "entra-client",
		ClientAuthMethod: "none",
		CallbackHost:     "localhost",
		CallbackPort:     port,
	}
}

func TestLoginWithAPreRegisteredClientAndDiscoveredEndpoints(t *testing.T) {
	p := entraShaped(t)
	port := freePort(t)
	cfg := loginConfig(t, p, entraSettings(port))
	out := &bytes.Buffer{}
	cfg.Out = out

	tokens, err := Login(cfg)
	if err != nil {
		t.Fatalf("Login: %v\n%s", err, out)
	}
	if tokens.AccessToken != "granted" || tokens.RefreshToken == "" {
		t.Errorf("tokens = %+v", tokens)
	}

	// What was asked of the authorization endpoint.
	wantRedirect := fmt.Sprintf("http://localhost:%d/callback", port)
	for param, want := range map[string]string{
		"client_id":             "entra-client",
		"scope":                 "api://mcp-mail/.default offline_access",
		"redirect_uri":          wantRedirect,
		"response_type":         "code",
		"code_challenge_method": "S256",
	} {
		if got := p.authorizeReq.Get(param); got != want {
			t.Errorf("authorize %s = %q, want %q", param, got, want)
		}
	}
	// Not sent, by decision (ADR-0006).
	for _, param := range []string{"resource", "prompt"} {
		if p.authorizeReq.Has(param) {
			t.Errorf("authorize request carries %s=%q", param, p.authorizeReq.Get(param))
		}
	}

	// Nothing was registered, or even asked of a registration endpoint.
	if p.registrations != 0 || p.hitsOn("/register") != 0 {
		t.Errorf("registration was attempted (%d registered, %d requests)", p.registrations, p.hitsOn("/register"))
	}

	// The token request is a public client's.
	if got := p.tokenForm.Get("client_id"); got != "entra-client" {
		t.Errorf("token client_id = %q", got)
	}
	if p.tokenForm.Has("client_secret") {
		t.Error("a client_secret was sent for a public client")
	}
	if got := p.tokenForm.Get("redirect_uri"); got != wantRedirect {
		t.Errorf("token redirect_uri = %q", got)
	}

	// What the next session will read.
	stored, ok := LoadDiscovered(cfg.DiscoveryPath)
	if !ok {
		t.Fatal("no discovery result was stored")
	}
	want := Discovered{
		AuthorizeURL: p.URL + "/authorize",
		TokenURL:     p.URL + "/token",
		Scopes:       []string{"api://mcp-mail/.default", "offline_access"},
		RedirectURI:  wantRedirect,
		ServerURL:    p.URL + "/mcp",
	}
	if !reflect.DeepEqual(*stored, want) {
		t.Errorf("stored result:\n got %+v\nwant %+v", *stored, want)
	}

	// What the user is shown: each decision with its source.
	printed := out.String()
	for _, line := range []string{
		"Authorization server: " + p.URL + "/tenant-id/v2.0 (named by " + p.URL + "/prm)\n",
		"Client: entra-client (the pre-registered one from the configuration)\n",
		"Scopes: api://mcp-mail/.default offline_access (from the server's metadata, with offline_access added so the login can renew itself)\n",
	} {
		if !strings.Contains(printed, line) {
			t.Errorf("output lacks %q:\n%s", line, printed)
		}
	}
	for _, never := range []string{"granted", "the-auth-code", "Registering", "registration"} {
		if strings.Contains(printed, never) {
			t.Errorf("output contains %q:\n%s", never, printed)
		}
	}
}

// What is written in the configuration is what is sent: configured scopes are
// used as they are, with nothing added.
func TestConfiguredScopesAreSentAsWritten(t *testing.T) {
	p := entraShaped(t)
	settings := entraSettings(freePort(t))
	settings.Scopes = []string{"api://mcp-mail/Mail.Read"}
	cfg := loginConfig(t, p, settings)
	out := &bytes.Buffer{}
	cfg.Out = out

	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	if got := p.authorizeReq.Get("scope"); got != "api://mcp-mail/Mail.Read" {
		t.Errorf("scope = %q, want exactly the configured one", got)
	}
	if want := "Scopes: api://mcp-mail/Mail.Read (from the configuration)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out.String(), "offline_access") {
		t.Errorf("output speaks of offline_access:\n%s", out)
	}
}

// Discovery for a pre-registered client leaves nothing on the provider, so it
// is simply done again: what the server publishes now is what the login uses.
func TestPreRegisteredClientDiscoversAtEveryLogin(t *testing.T) {
	p := entraShaped(t)
	cfg := loginConfig(t, p, entraSettings(freePort(t)))

	if _, err := Login(cfg); err != nil {
		t.Fatalf("first login: %v", err)
	}
	// The server changes what it asks for between the two logins.
	p.route("/prm", serveJSON(map[string]any{
		"resource":              p.URL + "/mcp",
		"authorization_servers": []string{p.URL + "/tenant-id/v2.0"},
		"scopes_supported":      []string{"api://mcp-mail/Mail.Send"},
	}))
	if _, err := Login(cfg); err != nil {
		t.Fatalf("second login: %v", err)
	}

	if n := p.hitsOn("/prm"); n != 2 {
		t.Errorf("the server's metadata was fetched %d times over two logins, want 2", n)
	}
	if got := p.authorizeReq.Get("scope"); got != "api://mcp-mail/Mail.Send offline_access" {
		t.Errorf("second login asked for %q, not what the server now publishes", got)
	}
}

// Two servers sharing one client and one port are logged in to one after the
// other: the port is free again once a login ends, and each keeps its own
// stored result.
func TestTwoServersShareAClientAndAPort(t *testing.T) {
	port := freePort(t)
	mail, calendar := entraShaped(t), entraShaped(t)
	mailCfg := loginConfig(t, mail, entraSettings(port))
	calendarCfg := loginConfig(t, calendar, entraSettings(port))

	if _, err := Login(mailCfg); err != nil {
		t.Fatalf("first server: %v", err)
	}
	if _, err := Login(calendarCfg); err != nil {
		t.Fatalf("second server, same port: %v", err)
	}

	for name, c := range map[string]struct {
		cfg LoginConfig
		p   *fakeProvider
	}{"mail": {mailCfg, mail}, "calendar": {calendarCfg, calendar}} {
		stored, ok := LoadDiscovered(c.cfg.DiscoveryPath)
		if !ok || stored.ServerURL != c.p.URL+"/mcp" || stored.TokenURL != c.p.URL+"/token" {
			t.Errorf("%s: stored result is not its own: %+v", name, stored)
		}
	}
}

// When discovery fails for a pre-registered client, the way forward is to
// write the endpoints. Advice about registering an app would be wrong: the
// client is already registered.
func TestFailedDiscoveryForAPreRegisteredClientNamesTheEndpoints(t *testing.T) {
	p := newFakeProvider(t)
	p.route("/mcp", challengeWith(`Bearer realm="mcp"`))
	p.route("/.well-known/oauth-authorization-server", http.NotFound)

	_, err := Login(loginConfig(t, p, entraSettings(freePort(t))))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "add oauth.authorizeUrl and oauth.tokenUrl") {
		t.Errorf("error does not name the fix: %v", err)
	}
	for _, never := range []string{"register an OAuth app", "dynamic client registration", "oauth.clientSecret"} {
		if strings.Contains(err.Error(), never) {
			t.Errorf("error gives advice for a client that is not registered (%q): %v", never, err)
		}
	}
}

// A dynamic registration belongs to the server it was made for. A stored one
// for another URL, or one from before the URL was recorded, is not reused.
func TestRegistrationIsNotReusedForAnotherServer(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{CallbackPort: freePort(t)})

	if _, err := Login(cfg); err != nil {
		t.Fatalf("first login: %v", err)
	}
	if _, err := Login(cfg); err != nil {
		t.Fatalf("same server again: %v", err)
	}
	if p.registrations != 1 {
		t.Fatalf("registrations after two logins to one server = %d, want 1", p.registrations)
	}

	cfg.ServerURL = p.URL + "/another/mcp"
	if _, err := Login(cfg); err != nil {
		t.Fatalf("other server: %v", err)
	}
	if p.registrations != 2 {
		t.Errorf("registrations after a login to another url = %d, want 2", p.registrations)
	}
}

func TestRegistrationFromBeforeTheURLWasRecordedIsNotReused(t *testing.T) {
	p := newFakeProvider(t)
	port := freePort(t)
	cfg := loginConfig(t, p, Settings{CallbackPort: port})

	// The stored form of earlier versions: everything but server_url.
	old := fmt.Sprintf(`{"authorize_url":%q,"token_url":%q,"client_id":"old-client","redirect_uri":"http://127.0.0.1:%d/callback"}`,
		p.URL+"/authorize", p.URL+"/token", port)
	if err := os.WriteFile(cfg.DiscoveryPath, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	if p.registrations != 1 {
		t.Errorf("registrations = %d, want 1 (the old result must not be reused)", p.registrations)
	}
	if got := p.authorizeReq.Get("client_id"); got != "registered-client" {
		t.Errorf("client_id = %q, want the newly registered one", got)
	}
}

// A reused registration did not go through discovery, so its scopes are shown
// as what they are: the stored ones.
func TestReusedRegistrationReportsItsStoredScopes(t *testing.T) {
	p := newFakeProvider(t)
	cfg := loginConfig(t, p, Settings{CallbackPort: freePort(t)})
	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	cfg.Out = out
	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	if want := "Scopes: read write (from the stored registration)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out.String(), "Authorization server:") {
		t.Errorf("a reused registration reported a discovery that did not run:\n%s", out)
	}
}

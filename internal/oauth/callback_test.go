package oauth

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// hasLoopbackV6 reports whether this machine can listen on ::1 at all. Tests
// that need the address skip without it, as a container without IPv6 has none.
func hasLoopbackV6() bool {
	l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// The redirect URI is what the provider compares against the registration, so
// each combination of scheme and host must produce exactly its string.
func TestRedirectURIFollowsSchemeAndHost(t *testing.T) {
	cases := []struct {
		scheme, host string
		want         string
	}{
		{"", "", "http://127.0.0.1:%d/callback"},
		{"http", "localhost", "http://localhost:%d/callback"},
		{"https", "", "https://localhost:%d/callback"},
		{"https", "127.0.0.1", "https://127.0.0.1:%d/callback"},
	}
	for _, tc := range cases {
		t.Run(tc.scheme+"/"+tc.host, func(t *testing.T) {
			port := freePort(t)
			cb, err := startCallbackServer(Settings{
				CallbackPort: port, CallbackScheme: tc.scheme, CallbackHost: tc.host,
			}, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			defer cb.close()
			if want := fmt.Sprintf(tc.want, port); cb.redirectURI != want {
				t.Errorf("redirect URI = %q, want %q", cb.redirectURI, want)
			}
		})
	}
}

// "localhost" is the browser's to resolve, so the callback has to be reachable
// at both loopback addresses.
func TestLocalhostCallbackAnswersOnBothLoopbackAddresses(t *testing.T) {
	if !hasLoopbackV6() {
		t.Skip("no IPv6 loopback on this machine")
	}
	var out bytes.Buffer
	cb, err := startCallbackServer(Settings{CallbackHost: "localhost"}, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer cb.close()
	cb.expect("the-state")

	_, port, _ := net.SplitHostPort(strings.TrimSuffix(strings.TrimPrefix(cb.redirectURI, "http://"), "/callback"))
	for _, addr := range []string{"127.0.0.1", "[::1]"} {
		resp, err := http.Get("http://" + addr + ":" + port + "/callback?state=wrong")
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		resp.Body.Close()
		// A wrong state is answered by this listener's own handler, which is
		// the proof that the request arrived here.
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 from the callback handler", addr, resp.StatusCode)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a clean start printed: %q", out.String())
	}
}

// The IP literal names one address, so nothing else is opened for it.
func TestIPLiteralCallbackDoesNotListenOnV6(t *testing.T) {
	if !hasLoopbackV6() {
		t.Skip("no IPv6 loopback on this machine")
	}
	port := freePort(t)
	cb, err := startCallbackServer(Settings{CallbackPort: port}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	defer cb.close()

	other, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("the callback for 127.0.0.1 also holds [::1]:%d: %v", port, err)
	}
	other.Close()
}

// When the port is taken on ::1 the login still works over 127.0.0.1, and the
// one line it prints has to name the port and the step that reaches it.
func TestTakenV6PortIsReportedAndTheLoginGoesOn(t *testing.T) {
	if !hasLoopbackV6() {
		t.Skip("no IPv6 loopback on this machine")
	}
	port := freePort(t)
	squatter, err := net.Listen("tcp", net.JoinHostPort("::1", strconv.Itoa(port)))
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()

	var out bytes.Buffer
	cb, err := startCallbackServer(Settings{CallbackPort: port, CallbackHost: "localhost"}, &out)
	if err != nil {
		t.Fatalf("a taken [::1] port stopped the login: %v", err)
	}
	defer cb.close()

	note := out.String()
	for _, want := range []string{fmt.Sprintf("[::1]:%d", port), `replace "localhost" with "127.0.0.1"`} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q does not contain %q", note, want)
		}
	}
	if strings.Count(note, "\n") != 1 {
		t.Errorf("the note is not one line: %q", note)
	}

	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/callback?state=wrong", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400 from the callback handler", resp.StatusCode)
	}
}

// A whole login with the redirect URI an Entra ID app is registered with.
func TestLoginWithLocalhostHTTPCallback(t *testing.T) {
	p := newFakeProvider(t)
	port := freePort(t)
	cfg := loginConfig(t, p, Settings{
		AuthorizeURL: p.URL + "/authorize",
		TokenURL:     p.URL + "/token",
		ClientID:     "pre-registered",
		CallbackPort: port,
		CallbackHost: "localhost",
	})

	if _, err := Login(cfg); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("http://localhost:%d/callback", port)
	if got := p.authorizeReq.Get("redirect_uri"); got != want {
		t.Errorf("authorize redirect_uri = %q, want %q", got, want)
	}
	// The token request must repeat the same string (RFC 6749 §4.1.3).
	if got := p.tokenForm.Get("redirect_uri"); got != want {
		t.Errorf("token redirect_uri = %q, want %q", got, want)
	}
}

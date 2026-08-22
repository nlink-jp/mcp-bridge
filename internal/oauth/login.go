package oauth

import (
	"crypto/subtle"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// defaultLoginTimeout bounds the wait for the user to finish in the browser.
const defaultLoginTimeout = 5 * time.Minute

// Settings are the OAuth parameters of one server, decoupled from the config
// file's shape so this package can be exercised without one.
//
// An empty Settings means "discover everything": the endpoints via RFC 8414
// and the client via RFC 7591.
type Settings struct {
	AuthorizeURL     string
	TokenURL         string
	ClientID         string
	ClientSecret     string
	Scopes           []string
	ExtraParams      map[string]string
	CallbackPort     int
	CallbackScheme   string
	ClientAuthMethod string
}

// complete reports whether the settings identify a pre-registered client, so
// no discovery is needed.
func (s Settings) complete() bool {
	return s.AuthorizeURL != "" && s.TokenURL != "" && s.ClientID != ""
}

// LoginConfig describes one interactive login.
type LoginConfig struct {
	ServerName    string
	ServerURL     string // the MCP endpoint, used only for discovery
	Settings      Settings
	TokensPath    string
	DiscoveryPath string

	HTTPClient  *http.Client
	Out         io.Writer                 // user-facing progress
	OpenBrowser func(rawURL string) error // replaced in tests
	Timeout     time.Duration
}

// Login runs the authorization_code flow with PKCE and stores the result.
//
// The callback listener is started before anything else, because its port is
// part of the redirect URI that dynamic client registration has to be told
// about.
func Login(cfg LoginConfig) (*Tokens, error) {
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultLoginTimeout
	}
	if cfg.OpenBrowser == nil {
		cfg.OpenBrowser = openBrowser
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = defaultHTTPClient()
	}

	callback, err := startCallbackServer(cfg.Settings, cfg.Out)
	if err != nil {
		return nil, err
	}
	defer callback.close()

	settings := cfg.Settings
	if !settings.complete() {
		discovered, err := discover(discoveryRequest{
			serverURL:   cfg.ServerURL,
			redirectURI: callback.redirectURI,
			cachePath:   cfg.DiscoveryPath,
			client:      cfg.HTTPClient,
			out:         cfg.Out,
		})
		if err != nil {
			return nil, err
		}
		settings = discovered.merge(settings)
	}

	verifier, err := newCodeVerifier()
	if err != nil {
		return nil, err
	}
	stateParam, err := newStateParam()
	if err != nil {
		return nil, err
	}
	callback.expect(stateParam)

	authURL, err := authorizeURL(settings, callback.redirectURI, stateParam, codeChallenge(verifier))
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(cfg.Out, "Opening a browser to authorize %s.\n", cfg.ServerName)
	fmt.Fprintf(cfg.Out, "If it does not open, visit this URL:\n\n%s\n\n", authURL)
	if err := cfg.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(cfg.Out, "Could not open a browser automatically (%v) — use the URL above.\n", err)
	}

	fmt.Fprintln(cfg.Out, "Waiting for the authorization callback...")
	code, err := callback.wait(cfg.Timeout)
	if err != nil {
		return nil, err
	}

	fmt.Fprintln(cfg.Out, "Exchanging the authorization code for tokens...")
	tokens, err := exchange(tokenRequest{
		client: cfg.HTTPClient,
		form: url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {code},
			"redirect_uri":  {callback.redirectURI},
			"code_verifier": {verifier},
		},
		tokenURL:         settings.TokenURL,
		clientID:         settings.ClientID,
		clientSecret:     settings.ClientSecret,
		clientAuthMethod: settings.ClientAuthMethod,
	})
	if err != nil {
		return nil, err
	}

	if err := SaveTokens(cfg.TokensPath, tokens); err != nil {
		return nil, err
	}

	fmt.Fprintf(cfg.Out, "Logged in to %s. %s\n", cfg.ServerName, tokens.Describe())
	return tokens, nil
}

// authorizeURL builds the authorization request.
//
// Extra parameters are applied first and the protocol parameters last, so a
// stray "state" or "redirect_uri" in extraParams cannot override the ones
// this flow depends on.
func authorizeURL(s Settings, redirectURI, stateParam, challenge string) (string, error) {
	base, err := url.Parse(s.AuthorizeURL)
	if err != nil {
		return "", fmt.Errorf("parse authorize URL %q: %w", s.AuthorizeURL, err)
	}
	query := base.Query()
	for k, v := range s.ExtraParams {
		query.Set(k, v)
	}
	if len(s.Scopes) > 0 {
		query.Set("scope", strings.Join(s.Scopes, " "))
	}
	query.Set("response_type", "code")
	query.Set("client_id", s.ClientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("state", stateParam)
	query.Set("code_challenge", challenge)
	query.Set("code_challenge_method", "S256")
	base.RawQuery = query.Encode()
	return base.String(), nil
}

// callbackServer is the loopback listener the provider redirects back to.
type callbackServer struct {
	redirectURI string
	server      *http.Server
	listener    net.Listener

	stateParam string
	codes      chan string
	errs       chan error
}

// startCallbackServer binds the loopback listener and serves /callback.
//
// A fixed port is used when the settings name one: a pre-registered OAuth app
// declares an exact redirect URI, so the port cannot vary between logins.
// Otherwise the OS picks one, which is fine for dynamic registration because
// the client is registered fresh with whatever port was chosen.
func startCallbackServer(s Settings, out io.Writer) (*callbackServer, error) {
	tcp, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.CallbackPort))
	if err != nil {
		if s.CallbackPort != 0 {
			return nil, fmt.Errorf("cannot listen on the configured callback port %d: %w "+
				"(another process may hold it; change oauth.callbackPort or pass --callback-port)", s.CallbackPort, err)
		}
		return nil, fmt.Errorf("start the OAuth callback listener: %w", err)
	}

	port := tcp.Addr().(*net.TCPAddr).Port
	scheme, host := "http", "127.0.0.1"
	listener := net.Listener(tcp)

	if s.CallbackScheme == "https" {
		cert, err := loopbackCert()
		if err != nil {
			tcp.Close()
			return nil, err
		}
		listener = tls.NewListener(tcp, &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		})
		scheme = "https"
		// Providers that restrict redirect URIs to host names accept
		// "localhost" where they reject the IP literal; the certificate
		// covers both.
		host = "localhost"
		fmt.Fprintln(out, "Note: the browser will warn that the callback connection is not secure. "+
			"That is the self-signed certificate for this loopback listener — continuing is expected.")
	}

	cb := &callbackServer{
		redirectURI: fmt.Sprintf("%s://%s:%d/callback", scheme, host, port),
		listener:    listener,
		codes:       make(chan string, 1),
		errs:        make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cb.handle)
	cb.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = cb.server.Serve(listener) }()

	return cb, nil
}

// expect records the state parameter the callback must carry.
func (c *callbackServer) expect(stateParam string) { c.stateParam = stateParam }

func (c *callbackServer) handle(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	// Constant-time comparison: the state parameter is the CSRF defence, and
	// comparing it byte by byte with an early exit leaks its prefix.
	if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(c.stateParam)) != 1 {
		c.fail(w, http.StatusBadRequest, "State mismatch",
			fmt.Errorf("the authorization callback carried the wrong state parameter, which can mean a cross-site request forgery attempt; nothing was stored"))
		return
	}

	if provider := query.Get("error"); provider != "" {
		desc := query.Get("error_description")
		c.fail(w, http.StatusOK, fmt.Sprintf("Authorization failed: %s", html.EscapeString(provider)),
			fmt.Errorf("the provider refused authorization: %s: %s", provider, orDash(desc)))
		return
	}

	code := query.Get("code")
	if code == "" {
		c.fail(w, http.StatusBadRequest, "No authorization code",
			fmt.Errorf("the authorization callback carried no code"))
		return
	}

	select {
	case c.codes <- code:
	default: // a second callback for the same login; the first one won
	}
	writePage(w, http.StatusOK, "Authorized", "You can close this tab and return to the terminal.")
}

// fail reports one problem to both the browser and the waiting command.
func (c *callbackServer) fail(w http.ResponseWriter, status int, heading string, err error) {
	select {
	case c.errs <- err:
	default:
	}
	writePage(w, status, heading, "Return to the terminal for details.")
}

// wait blocks until the provider calls back, the flow fails, or time runs out.
func (c *callbackServer) wait(timeout time.Duration) (string, error) {
	select {
	case code := <-c.codes:
		return code, nil
	case err := <-c.errs:
		return "", err
	case <-time.After(timeout):
		return "", fmt.Errorf("no authorization callback arrived within %s", timeout)
	}
}

func (c *callbackServer) close() {
	if c.server != nil {
		_ = c.server.Close()
	}
}

// writePage renders the minimal page the user sees in the browser.
func writePage(w http.ResponseWriter, status int, heading, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=\"utf-8\"><title>mcp-bridge</title>"+
		"<body style=\"font-family:system-ui,sans-serif;margin:4rem auto;max-width:32rem\">"+
		"<h1>%s</h1><p>%s</p></body>", html.EscapeString(heading), html.EscapeString(detail))
}

// openBrowser opens a URL with the platform's handler.
func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "linux":
		cmd = exec.Command("xdg-open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		return fmt.Errorf("no known browser command for %s", runtime.GOOS)
	}
	return cmd.Start()
}

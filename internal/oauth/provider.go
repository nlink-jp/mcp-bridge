package oauth

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"
)

// refreshMargin renews a token slightly before it expires, so one does not
// lapse between the check and the request that uses it.
const refreshMargin = 30 * time.Second

// ProviderConfig configures a token provider over a stored login.
type ProviderConfig struct {
	ServerName       string // named in errors so the fix is a command the user can copy
	TokensPath       string
	TokenURL         string
	ClientID         string
	ClientSecret     string
	ClientAuthMethod string

	HTTPClient *http.Client
	Now        func() time.Time
	Logs       io.Writer
}

// Provider supplies Bearer tokens from a stored login, refreshing them when
// it can. It satisfies the transport's TokenProvider interface.
type Provider struct {
	cfg ProviderConfig

	mu sync.Mutex
	// rejected records that the server refused this credential, so a later
	// failure can say that rather than reporting an empty token file.
	rejected bool
	tokens   *Tokens
}

// NewProvider loads the stored tokens for a server.
//
// A missing token file is the normal state before the first login, so the
// error says exactly which command fixes it rather than reporting a file that
// the user never created by hand.
func NewProvider(cfg ProviderConfig) (*Provider, error) {
	tokens, err := LoadTokens(cfg.TokensPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("not logged in to %q: run \"mcp-bridge login %s\" first", cfg.ServerName, cfg.ServerName)
		}
		return nil, fmt.Errorf("read stored tokens for %q: %w", cfg.ServerName, err)
	}
	if tokens.AccessToken == "" {
		return nil, fmt.Errorf("stored login for %q has no access token: run \"mcp-bridge login %s\" again", cfg.ServerName, cfg.ServerName)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Provider{cfg: cfg, tokens: tokens}, nil
}

// Token returns a usable access token, refreshing first if that is possible
// and necessary.
func (p *Provider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// A credential the server has already refused, with no way to renew it,
	// is a dead end. Saying so beats reporting a missing access token, which
	// is only the after-effect of Invalidate and reads like an empty file.
	if p.rejected && !p.tokens.Refreshable() {
		return "", fmt.Errorf("the stored login for %q was rejected by the server and there is no refresh token to renew it: run \"mcp-bridge login %s\"",
			p.cfg.ServerName, p.cfg.ServerName)
	}

	// Without a refresh token there is nothing this code could do about an
	// expiry, so the stored one is not actionable: return the token and let
	// the server be the judge. Failing here instead would turn a working
	// non-expiring token — Slack with rotation disabled issues exactly that —
	// into an hourly re-login for no reason (ADR-0003).
	if !p.tokens.Refreshable() {
		if p.tokens.AccessToken == "" {
			return "", fmt.Errorf("no access token for %q: run \"mcp-bridge login %s\"", p.cfg.ServerName, p.cfg.ServerName)
		}
		return p.tokens.AccessToken, nil
	}

	if p.tokens.AccessToken != "" && !p.tokens.Expired(p.cfg.Now(), refreshMargin) {
		return p.tokens.AccessToken, nil
	}

	refreshed, err := p.refresh()
	if err != nil {
		return "", fmt.Errorf("refreshing the token for %q failed (run \"mcp-bridge login %s\" to log in again): %w",
			p.cfg.ServerName, p.cfg.ServerName, err)
	}
	return refreshed, nil
}

// Invalidate discards the cached access token so the next Token call renews
// it. The transport calls this after a 401.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rejected = true
	p.tokens.AccessToken = ""
	p.tokens.ExpiresAt = 0
}

// refresh exchanges the refresh token. The caller holds the lock.
func (p *Provider) refresh() (string, error) {
	if p.cfg.TokenURL == "" {
		return "", fmt.Errorf("no token endpoint is configured")
	}
	fresh, err := exchange(tokenRequest{
		client: p.cfg.HTTPClient,
		form: url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {p.tokens.RefreshToken},
		},
		tokenURL:         p.cfg.TokenURL,
		clientID:         p.cfg.ClientID,
		clientSecret:     p.cfg.ClientSecret,
		clientAuthMethod: p.cfg.ClientAuthMethod,
		now:              p.cfg.Now,
	})
	if err != nil {
		return "", err
	}

	// A provider that does not rotate refresh tokens omits the field; keeping
	// the old one is what lets the next refresh work.
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = p.tokens.RefreshToken
	}
	p.tokens = fresh
	p.rejected = false

	// A token that cannot be persisted still works for this session, so this
	// is a warning rather than a failure — but it is worth saying, because
	// every later session will have to refresh again.
	if err := SaveTokens(p.cfg.TokensPath, fresh); err != nil {
		p.logf("warning: refreshed tokens could not be saved: %v", err)
	}
	return fresh.AccessToken, nil
}

func (p *Provider) logf(format string, args ...any) {
	if p.cfg.Logs == nil {
		return
	}
	fmt.Fprintf(p.cfg.Logs, "mcp-bridge: "+format+"\n", args...)
}

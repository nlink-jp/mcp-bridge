package oauth

import (
	"errors"
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

	// lockWait shortens the wait for the shared lock in tests; zero means
	// the production limit.
	lockWait time.Duration
}

// Provider supplies Bearer tokens from a stored login, refreshing them when
// it can. It satisfies the transport's TokenProvider interface.
//
// Several mcp-bridge processes for one server share the token file, so the
// file is the one authoritative copy of the login and what a Provider holds
// is a copy of it (ADR-0007). Whenever the copy will not do, the file is read
// again under a lock shared by every process, and the decision is made from
// what it says then.
type Provider struct {
	cfg ProviderConfig

	mu sync.Mutex
	// rejected records that the server refused this credential, so a later
	// failure can say that rather than reporting an empty token file.
	rejected bool
	// rejectedAccess is the access token the server refused, so that the
	// same token, still in the file, is not taken for another process's
	// renewal.
	rejectedAccess string
	// unsavedFrom is set while this process's last renewal could not be
	// saved: it is the refresh token that renewal failed to replace in the
	// file. While the file still holds it, the newer copy is in memory.
	unsavedFrom string
	tokens      *Tokens
}

// NewProvider loads the stored tokens for a server.
//
// A missing token file is the normal state before the first login, so the
// error says exactly which command fixes it rather than reporting a file that
// the user never created by hand.
func NewProvider(cfg ProviderConfig) (*Provider, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.lockWait == 0 {
		cfg.lockWait = lockWait
	}
	p := &Provider{cfg: cfg}

	// The first read is taken under the lock too, so it never sees the file
	// in the middle of being replaced (on Windows a rename over an open file
	// can fail). An unusable lock is reported at the first renewal, not here.
	unlock, err := lockTokens(cfg.TokensPath, cfg.lockWait)
	switch {
	case err == nil:
		defer unlock()
	case errors.Is(err, errLockTimeout):
		return nil, p.busyError(err)
	}

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
	p.tokens = tokens
	return p, nil
}

// Token returns a usable access token, refreshing first if that is possible
// and necessary.
func (p *Provider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.rejected {
		// Without a refresh token there is nothing this code could do about
		// an expiry, so the stored one is not actionable: return the token
		// and let the server be the judge. Failing here instead would turn a
		// working non-expiring token — Slack with rotation disabled issues
		// exactly that — into an hourly re-login for no reason (ADR-0003).
		if !p.tokens.Refreshable() && p.tokens.AccessToken != "" {
			return p.tokens.AccessToken, nil
		}
		if p.tokens.Refreshable() && p.tokens.AccessToken != "" && !p.tokens.Expired(p.cfg.Now(), refreshMargin) {
			return p.tokens.AccessToken, nil
		}
	}
	return p.decideUnderLock()
}

// decideUnderLock reads the token file again under the shared lock and
// decides from it whether to adopt what is there, renew, or give up
// (ADR-0007, decision 2). The caller holds p.mu.
func (p *Provider) decideUnderLock() (string, error) {
	unlock, err := lockTokens(p.cfg.TokensPath, p.cfg.lockWait)
	switch {
	case err == nil:
		defer unlock()
	case errors.Is(err, errLockTimeout):
		// Renewing without the lock now is the race the lock prevents:
		// whoever holds it may be presenting this very refresh token.
		return "", p.busyError(err)
	default:
		p.logf("warning: cannot lock the token file for %q, so renewals are not coordinated with other mcp-bridge processes: %v",
			p.cfg.ServerName, err)
	}

	name := p.cfg.ServerName
	disk, err := LoadTokens(p.cfg.TokensPath)
	if err != nil {
		// Renewing from memory here would write back a login that was
		// logged out, or paper over a file someone edited by hand.
		if os.IsNotExist(err) {
			return "", fmt.Errorf("not logged in to %q (the stored login was removed): run \"mcp-bridge login %s\"", name, name)
		}
		return "", fmt.Errorf("the stored login for %q cannot be read (run \"mcp-bridge login %s\" to log in again): %w", name, name, err)
	}

	// This process renewed but could not save the result, and nobody has
	// rewritten the file since: the newer copy is the one in memory, and the
	// file's refresh token may already be spent.
	if p.unsavedFrom != "" && disk.RefreshToken == p.unsavedFrom {
		return p.renew(p.tokens.RefreshToken)
	}
	p.unsavedFrom = ""

	usable := disk.AccessToken != "" && disk.AccessToken != p.rejectedAccess &&
		(!disk.Refreshable() || !disk.Expired(p.cfg.Now(), refreshMargin))
	if usable {
		adopted := disk.AccessToken != p.tokens.AccessToken
		p.tokens, p.rejected, p.rejectedAccess = disk, false, ""
		if adopted {
			p.logf("using the token another mcp-bridge process stored for %q (%s)", name, disk.Describe())
		}
		return disk.AccessToken, nil
	}

	if disk.Refreshable() {
		p.tokens = disk
		return p.renew(disk.RefreshToken)
	}

	if p.rejected {
		return "", fmt.Errorf("the stored login for %q was rejected by the server and there is no refresh token to renew it: run \"mcp-bridge login %s\"",
			name, name)
	}
	return "", fmt.Errorf("no access token for %q: run \"mcp-bridge login %s\"", name, name)
}

// busyError reports a lock another process kept past the limit.
func (p *Provider) busyError(err error) error {
	return fmt.Errorf("another mcp-bridge process is renewing the token for %q; try again (%w)", p.cfg.ServerName, err)
}

// Invalidate discards the cached access token so the next Token call renews
// it. The transport calls this after a 401.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rejected = true
	p.rejectedAccess = p.tokens.AccessToken
	p.tokens.AccessToken = ""
	p.tokens.ExpiresAt = 0
}

// renew exchanges the refresh token sent. The caller holds p.mu and, unless
// it could not be taken, the file lock.
func (p *Provider) renew(sent string) (string, error) {
	name := p.cfg.ServerName
	if p.cfg.TokenURL == "" {
		return "", fmt.Errorf("refreshing the token for %q failed (run \"mcp-bridge login %s\" to log in again): no token endpoint is configured", name, name)
	}
	fresh, err := exchange(tokenRequest{
		client: p.cfg.HTTPClient,
		form: url.Values{
			"grant_type":    {"refresh_token"},
			"refresh_token": {sent},
		},
		tokenURL:         p.cfg.TokenURL,
		clientID:         p.cfg.ClientID,
		clientSecret:     p.cfg.ClientSecret,
		clientAuthMethod: p.cfg.ClientAuthMethod,
		now:              p.cfg.Now,
	})
	if err != nil {
		// No access token and no expiry: the next call comes back here and
		// reads the file again, so neither a rejected token nor a stale
		// login is handed out, and a login made meanwhile is picked up.
		p.tokens = &Tokens{RefreshToken: sent, TokenType: p.tokens.TokenType}
		return "", fmt.Errorf("refreshing the token for %q failed (run \"mcp-bridge login %s\" to log in again): %w", name, name, err)
	}

	// A provider that does not rotate refresh tokens omits the field; keeping
	// the old one is what lets the next refresh work.
	rotation := "the server issued a new refresh token"
	if fresh.RefreshToken == "" || fresh.RefreshToken == sent {
		fresh.RefreshToken = sent
		rotation = "the refresh token was kept"
	}
	p.tokens, p.rejected, p.rejectedAccess = fresh, false, ""

	// A token that cannot be persisted still works for this session, so this
	// is a warning rather than a failure — but it is worth saying, because
	// every later session will have to refresh again.
	if err := SaveTokens(p.cfg.TokensPath, fresh); err != nil {
		p.logf("warning: refreshed tokens could not be saved: %v", err)
		if p.unsavedFrom == "" {
			p.unsavedFrom = sent
		}
	} else {
		p.unsavedFrom = ""
	}
	p.logf("renewed the token for %q (%s; %s)", name, fresh.Describe(), rotation)
	return fresh.AccessToken, nil
}

func (p *Provider) logf(format string, args ...any) {
	if p.cfg.Logs == nil {
		return
	}
	fmt.Fprintf(p.cfg.Logs, "mcp-bridge: "+format+"\n", args...)
}

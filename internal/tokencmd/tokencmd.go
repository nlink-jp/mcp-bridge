// Package tokencmd obtains a Bearer token by running an external command.
//
// It covers the credentials a user already has a tool for — `gcloud auth
// print-access-token`, `aws ...`, a vault client — without mcp-bridge growing
// an integration for each one.
package tokencmd

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// defaultTTL caches the command's output briefly. The commands this wraps are
// slow (often a network round trip of their own) and the tokens they return
// are short-lived but not that short.
const defaultTTL = 5 * time.Minute

// Provider runs a command and uses its standard output as the token.
type Provider struct {
	command string
	args    []string
	ttl     time.Duration
	now     func() time.Time

	mu      sync.Mutex
	token   string
	fetched time.Time
}

// New creates a provider. A zero ttl means the default.
func New(command string, args []string, ttl time.Duration) *Provider {
	if ttl == 0 {
		ttl = defaultTTL
	}
	return &Provider{command: command, args: args, ttl: ttl, now: time.Now}
}

// Token returns a cached token, or runs the command to get one.
func (p *Provider) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.token != "" && p.now().Sub(p.fetched) < p.ttl {
		return p.token, nil
	}

	// The command is executed directly, never through a shell: the command
	// and its arguments come from a config file, and a shell would turn a
	// value containing a semicolon into a second command.
	cmd := exec.Command(p.command, p.args...)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("token command %q failed: %w; stderr: %s",
				p.describe(), err, truncate(string(exitErr.Stderr), 200))
		}
		return "", fmt.Errorf("token command %q failed: %w", p.describe(), err)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("token command %q produced no output", p.describe())
	}
	// A command that prints several lines has almost certainly printed
	// something other than a token; using the first line silently would send
	// a broken credential upstream.
	if strings.ContainsAny(token, "\n\r") {
		return "", fmt.Errorf("token command %q produced multi-line output, which is not a token: %s",
			p.describe(), truncate(token, 120))
	}

	p.token = token
	p.fetched = p.now()
	return token, nil
}

// Invalidate discards the cached token so the next call re-runs the command.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = ""
	p.fetched = time.Time{}
}

// describe renders the command for an error message.
func (p *Provider) describe() string {
	if len(p.args) == 0 {
		return p.command
	}
	return p.command + " " + strings.Join(p.args, " ")
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

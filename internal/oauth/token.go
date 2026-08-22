// Package oauth implements the OAuth 2.0 authorization_code flow that
// mcp-bridge exists for: PKCE against a client the user registered by hand,
// with RFC 8414 metadata discovery and RFC 7591 dynamic registration for the
// providers that support them.
package oauth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Tokens is what a login produces and a session consumes.
//
// ExpiresAt is a Unix timestamp, and zero means "no known expiry" rather than
// "expired at the epoch". That distinction is the whole of ADR-0003: a
// provider with token rotation disabled (Slack, notably) returns neither
// expires_in nor a refresh_token, and inventing an expiry for such a token
// forces a re-login every hour for no reason. A genuine revocation shows up
// as a 401 from the server, which is where it belongs.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	ExpiresAt    int64  `json:"expires_at"`
}

// Refreshable reports whether these tokens can be renewed without the user.
func (t *Tokens) Refreshable() bool { return t.RefreshToken != "" }

// Expired reports whether the access token is past its stored expiry, with a
// margin so a token does not expire in flight. Tokens with no known expiry
// are never considered expired here.
func (t *Tokens) Expired(now time.Time, margin time.Duration) bool {
	if t.ExpiresAt == 0 {
		return false
	}
	return !now.Add(margin).Before(time.Unix(t.ExpiresAt, 0))
}

// Describe summarises the tokens for a human, without printing any of them.
func (t *Tokens) Describe() string {
	switch {
	case t.ExpiresAt == 0 && !t.Refreshable():
		return "no expiry reported and no refresh token: the token is used until the server rejects it"
	case t.ExpiresAt == 0:
		return "no expiry reported; a refresh token is stored"
	case t.Refreshable():
		return fmt.Sprintf("expires %s; renews automatically", time.Unix(t.ExpiresAt, 0).Format(time.RFC3339))
	default:
		return fmt.Sprintf("expires %s and cannot be refreshed; log in again after that", time.Unix(t.ExpiresAt, 0).Format(time.RFC3339))
	}
}

// LoadTokens reads tokens from path.
func LoadTokens(path string) (*Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &t, nil
}

// SaveTokens writes tokens to path with mode 0600, atomically.
//
// The write goes to a temporary file in the same directory and is renamed
// into place, so a crash mid-write cannot leave a truncated token file that
// looks like a corrupt login.
func SaveTokens(path string, t *Tokens) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tokens-*.json")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("restrict token file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write tokens: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write tokens: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("install token file: %w", err)
	}
	return nil
}

// DeleteTokens removes a token file. A file that is already gone is success:
// logging out twice is not an error.
func DeleteTokens(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

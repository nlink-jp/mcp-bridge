package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// PKCE (RFC 7636) is applied on every login, including for confidential
// clients that also send a secret. It costs nothing and closes the
// authorization-code interception window regardless of client type.

// newCodeVerifier returns a random PKCE code verifier.
func newCodeVerifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// codeChallenge derives the S256 challenge for a verifier.
func codeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// newStateParam returns a random CSRF state value for the authorize request.
func newStateParam() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate state parameter: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

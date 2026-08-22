package oauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSaveTokensIsPrivateAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "slack", "tokens.json")
	tokens := &Tokens{AccessToken: "secret", RefreshToken: "r", ExpiresAt: 1234}

	if err := SaveTokens(path, tokens); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %o, want 600", perm)
	}

	loaded, err := LoadTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if *loaded != *tokens {
		t.Errorf("round trip lost data: %+v", loaded)
	}

	// No temporary file may survive the write.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tokens-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestDeleteTokensIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := SaveTokens(path, &Tokens{AccessToken: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := DeleteTokens(path); err != nil {
		t.Fatal(err)
	}
	// Logging out twice is not an error.
	if err := DeleteTokens(path); err != nil {
		t.Errorf("second delete failed: %v", err)
	}
}

// ADR-0003: zero means "no known expiry", not "expired at the epoch". A
// provider with rotation disabled returns neither expires_in nor a refresh
// token, and treating that as expired forces an hourly re-login.
func TestZeroExpiryIsNeverExpired(t *testing.T) {
	tokens := &Tokens{AccessToken: "x", ExpiresAt: 0}
	if tokens.Expired(time.Now(), refreshMargin) {
		t.Error("a token with no known expiry was treated as expired")
	}
	if tokens.Expired(time.Now().Add(100*365*24*time.Hour), refreshMargin) {
		t.Error("a token with no known expiry expired eventually")
	}
}

func TestExpiryBoundary(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	cases := []struct {
		name      string
		expiresAt int64
		want      bool
	}{
		{"comfortably valid", now.Add(time.Hour).Unix(), false},
		{"inside the margin", now.Add(10 * time.Second).Unix(), true},
		{"already past", now.Add(-time.Second).Unix(), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tokens := &Tokens{AccessToken: "x", ExpiresAt: tc.expiresAt}
			if got := tokens.Expired(now, refreshMargin); got != tc.want {
				t.Errorf("Expired = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExpiryFor(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if got := expiryFor(3600, "", now); got != now.Add(time.Hour).Unix() {
		t.Errorf("expires_in was not honoured: %d", got)
	}
	if got := expiryFor(0, "refresh", now); got != now.Add(time.Hour).Unix() {
		t.Errorf("a refreshable token with no expires_in should be re-probed in an hour: %d", got)
	}
	// The ADR-0003 case.
	if got := expiryFor(0, "", now); got != 0 {
		t.Errorf("a non-expiring, non-refreshable token was given an expiry: %d", got)
	}
}

func TestDescribeNamesTheConsequence(t *testing.T) {
	cases := []struct {
		tokens Tokens
		want   string
	}{
		{Tokens{}, "until the server rejects it"},
		{Tokens{RefreshToken: "r"}, "refresh token is stored"},
		{Tokens{RefreshToken: "r", ExpiresAt: 2_000_000_000}, "renews automatically"},
		{Tokens{ExpiresAt: 2_000_000_000}, "log in again"},
	}
	for _, tc := range cases {
		if got := tc.tokens.Describe(); !strings.Contains(got, tc.want) {
			t.Errorf("Describe() = %q, want it to mention %q", got, tc.want)
		}
	}
}

func TestLoadTokensReportsMissingFileAsNotExist(t *testing.T) {
	_, err := LoadTokens(filepath.Join(t.TempDir(), "absent.json"))
	if !os.IsNotExist(err) {
		t.Errorf("a missing token file must be distinguishable from a corrupt one: %v", err)
	}
}

func TestPKCEChallengeIsS256(t *testing.T) {
	verifier, err := newCodeVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		t.Errorf("verifier length %d is outside the RFC 7636 range", len(verifier))
	}
	// The known S256 example from RFC 7636 appendix B.
	const known = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	if got := codeChallenge(known); got != "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM" {
		t.Errorf("S256 challenge = %q, does not match RFC 7636 appendix B", got)
	}
	other, _ := newCodeVerifier()
	if verifier == other {
		t.Error("two verifiers came out identical")
	}
}

func TestStateParamIsRandom(t *testing.T) {
	a, err := newStateParam()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newStateParam()
	if a == b {
		t.Error("two state parameters came out identical")
	}
	if a == "" {
		t.Error("empty state parameter")
	}
}

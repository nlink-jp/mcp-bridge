package oauth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxTokenResponseBytes bounds what is read from a token endpoint. Token
// responses are small; anything larger is a misconfigured endpoint or an
// error page.
const maxTokenResponseBytes = 1 << 20

// tokenRequest describes one call to a token endpoint.
type tokenRequest struct {
	client           *http.Client
	tokenURL         string
	form             url.Values
	clientID         string
	clientSecret     string
	clientAuthMethod string
	now              func() time.Time
}

// exchange posts a grant to the token endpoint and returns the tokens it
// yields.
//
// The request is built by hand rather than with http.PostForm so that
// clientAuthMethod can route the credentials into an Authorization header
// instead of the form body.
func exchange(req tokenRequest) (*Tokens, error) {
	httpReq, err := http.NewRequest(http.MethodPost, req.tokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpReq.Header.Set("Accept", "application/json")

	applyClientAuth(httpReq, req.form, req.clientAuthMethod, req.clientID, req.clientSecret)

	encoded := req.form.Encode()
	httpReq.Body = io.NopCloser(strings.NewReader(encoded))
	httpReq.ContentLength = int64(len(encoded))

	client := req.client
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("token request to %s: %w", req.tokenURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned HTTP %d: %s", resp.StatusCode, describeOAuthError(body))
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}

	// Some providers answer HTTP 200 with an error object in the body —
	// Slack's oauth.v2.access is the well-known example. Treating that as
	// success stores an empty token and fails later as an opaque 401.
	if parsed.Error != "" {
		return nil, fmt.Errorf("token endpoint reported %q: %s", parsed.Error, orDash(parsed.ErrorDesc))
	}
	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("token response contained no access_token: %s", truncate(body, 200))
	}

	now := req.now
	if now == nil {
		now = time.Now
	}

	return &Tokens{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		TokenType:    parsed.TokenType,
		ExpiresAt:    expiryFor(parsed.ExpiresIn, parsed.RefreshToken, now()),
	}, nil
}

// expiryFor decides what to record as the expiry.
//
//   - expires_in given: honour it.
//   - no expires_in but a refresh token: check back in an hour. The provider
//     can renew silently before then, so the guess costs nothing.
//   - neither: a non-expiring, non-refreshable token. Record zero — "no known
//     expiry" — and let a real revocation surface as a 401 from the server.
//     Inventing an hour here is what made the predecessor demand a fresh
//     login every hour from Slack for no reason (ADR-0003).
func expiryFor(expiresIn int, refreshToken string, now time.Time) int64 {
	switch {
	case expiresIn > 0:
		return now.Add(time.Duration(expiresIn) * time.Second).Unix()
	case refreshToken != "":
		return now.Add(time.Hour).Unix()
	default:
		return 0
	}
}

// describeOAuthError renders an RFC 6749 §5.2 error body readably, falling
// back to the raw payload when it is not one.
func describeOAuthError(body []byte) string {
	var parsed struct {
		Error     string `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error != "" {
		if parsed.ErrorDesc != "" {
			return fmt.Sprintf("%s: %s", parsed.Error, parsed.ErrorDesc)
		}
		return parsed.Error
	}
	return truncate(body, 200)
}

// defaultHTTPClient bounds every OAuth call. These are short request/response
// exchanges with no streaming, so one whole-request timeout is right.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

func orDash(s string) string {
	if s == "" {
		return "(no description)"
	}
	return s
}

func truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

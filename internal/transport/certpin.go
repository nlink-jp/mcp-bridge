package transport

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
)

// maxRedirects matches the limit net/http applies when CheckRedirect is unset.
const maxRedirects = 10

// WithPinnedCert accepts exactly the certificate whose SHA-256 is sum, in place
// of the usual verification.
func WithPinnedCert(sum []byte) Option {
	return func(t *httpTransport) {
		t.client = defaultClient(pinnedTLSConfig(sum))
		t.client.CheckRedirect = refuseRedirectOffHTTPS
	}
}

// refuseRedirectOffHTTPS keeps a pinned connection on TLS. Go carries the
// request's headers across a redirect to the same host whatever the scheme, so
// following one to http would send the credential in the clear. A redirect to
// another https server needs no rule here: it fails the pin.
func refuseRedirectOffHTTPS(req *http.Request, via []*http.Request) error {
	if req.URL.Scheme != "https" {
		return fmt.Errorf("refusing a redirect to %s: a connection pinned by tlsCertSha256 stays on https", req.URL.Redacted())
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return nil
}

// pinnedTLSConfig compares the presented certificate with the pin and does
// nothing else. Issuer, host name and validity are left unchecked on purpose:
// the devices this serves fail those checks by construction, and an exact match
// on the certificate already says what they would have said.
func pinnedTLSConfig(sum []byte) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // replaced by the pin check below
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("the server presented no TLS certificate")
			}
			got := sha256.Sum256(state.PeerCertificates[0].Raw)
			if bytes.Equal(got[:], sum) {
				return nil
			}
			return fmt.Errorf("the TLS certificate is not the one pinned by tlsCertSha256 (presented %s, configured %s); if it was replaced on purpose, set tlsCertSha256 to the presented value",
				formatFingerprint(got[:]), formatFingerprint(sum))
		},
	}
}

// formatFingerprint renders a digest the way openssl prints it, so the two can
// be compared by eye.
func formatFingerprint(sum []byte) string {
	return strings.ReplaceAll(fmt.Sprintf("% X", sum), " ", ":")
}

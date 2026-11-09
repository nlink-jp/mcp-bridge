package transport

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"strings"
)

// WithPinnedCert accepts exactly the certificate whose SHA-256 is sum, in place
// of the usual verification.
func WithPinnedCert(sum []byte) Option {
	return func(t *httpTransport) { t.client = defaultClient(pinnedTLSConfig(sum)) }
}

// pinnedTLSConfig compares the presented certificate with the pin and does
// nothing else. Issuer, host name and validity are left unchecked on purpose:
// the devices this serves fail those checks by construction, and an exact match
// on the certificate already says what they would have said.
//
// VerifyConnection rather than VerifyPeerCertificate, because the latter is
// skipped on a resumed session.
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

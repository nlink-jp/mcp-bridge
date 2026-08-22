package oauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"time"
)

// loopbackCert creates an ephemeral self-signed certificate for the local
// OAuth callback listener. It lives in memory only and is regenerated on
// every login.
//
// This exists because some providers — Slack most notably — reject an
// http:// loopback redirect URI when the OAuth app is registered, and accept
// only https://. On a loopback listener there is no realistic interception to
// defend against, so the choice is between a self-signed certificate and not
// supporting those providers at all.
//
// The browser shows a one-time "not secure" warning; clicking through is the
// expected path, and the login command says so before opening the browser.
//
// The SANs cover both ways a browser may reach the listener: the loopback IP
// literals, and the name "localhost", which is the form providers are most
// likely to accept in a redirect-URI allow list.
func loopbackCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate callback key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate certificate serial: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "mcp-bridge local OAuth callback"},
		// A minute of backdating tolerates small clock differences between
		// the listener and the browser.
		NotBefore:   time.Now().Add(-time.Minute),
		NotAfter:    time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		DNSNames:    []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create callback certificate: %w", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: &template}, nil
}

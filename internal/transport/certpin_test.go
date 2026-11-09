package transport

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const pinTestRequest = `{"jsonrpc":"2.0","id":1,"method":"ping"}`

// applianceCert builds the certificate a local device tends to serve:
// self-signed, issued to localhost only, and here also long expired. Each of
// the three is a reason usual verification refuses it.
func applianceCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// applianceServer serves that certificate on 127.0.0.1 and counts the requests
// that got as far as the handler.
func applianceServer(t *testing.T) (url string, pin []byte, requests *atomic.Int32) {
	t.Helper()
	cert := applianceCert(t)
	requests = &atomic.Int32{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0) // refused handshakes are the point
	srv.StartTLS()
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(cert.Certificate[0])
	return srv.URL, sum[:], requests
}

func TestPinAcceptsACertificateVerificationWouldRefuse(t *testing.T) {
	url, pin, requests := applianceServer(t)
	tr, err := NewHTTP(url, WithPinnedCert(pin))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	if err := tr.Send([]byte(pinTestRequest)); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := receiveWithin(t, tr, 5*time.Second); !strings.Contains(got, `"result"`) {
		t.Errorf("reply = %s", got)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1", requests.Load())
	}
}

// The control: nothing about the feature may loosen a server without a pin.
func TestWithoutAPinTheCertificateIsRefused(t *testing.T) {
	url, _, requests := applianceServer(t)
	tr, err := NewHTTP(url)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	err = tr.Send([]byte(pinTestRequest))
	if err == nil {
		t.Fatal("Send succeeded against an unverifiable certificate")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error does not mention the certificate: %v", err)
	}
	if requests.Load() != 0 {
		t.Errorf("requests = %d, want 0", requests.Load())
	}
}

// A different certificate is what an impostor looks like, so nothing may be
// sent to it. The credential rides in the request this test proves never left.
func TestAWrongPinStopsTheConnectionBeforeAnythingIsSent(t *testing.T) {
	url, pin, requests := applianceServer(t)
	wrong := sha256.Sum256([]byte("some other certificate"))

	tr, err := NewHTTP(url, WithPinnedCert(wrong[:]), WithHeaders(map[string]string{"Authorization": "Bearer secret"}))
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()

	err = tr.Send([]byte(pinTestRequest))
	if err == nil {
		t.Fatal("Send succeeded with the wrong pin")
	}
	if requests.Load() != 0 {
		t.Errorf("requests = %d, want 0: the request reached a server that failed the pin", requests.Load())
	}
	// Both values are in the error because replacing one with the other is the
	// repair when the device regenerated its certificate on purpose.
	for _, want := range []string{formatFingerprint(pin), formatFingerprint(wrong[:]), "tlsCertSha256"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q: %v", want, err)
		}
	}
}

func TestFingerprintIsFormattedAsOpensslPrintsIt(t *testing.T) {
	if got := formatFingerprint([]byte{0x95, 0x1f, 0x00}); got != "95:1F:00" {
		t.Errorf("got %q", got)
	}
}

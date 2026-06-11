package httpx

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateTLSFlags(t *testing.T) {
	if err := ValidateTLSFlags("", ""); err != nil {
		t.Errorf("both empty must be valid: %v", err)
	}
	if err := ValidateTLSFlags("c.pem", "k.pem"); err != nil {
		t.Errorf("both set must be valid: %v", err)
	}
	if err := ValidateTLSFlags("c.pem", ""); err == nil {
		t.Error("cert without key must error")
	}
	if err := ValidateTLSFlags("", "k.pem"); err == nil {
		t.Error("key without cert must error")
	}
}

// selfSignedCert writes a throwaway ECDSA certificate (valid for 127.0.0.1)
// and key to dir, returning their paths and the parsed certificate for the
// client trust pool.
func selfSignedCert(t *testing.T, dir string) (certFile, keyFile string, cert *x509.Certificate) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "promhash-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, cert
}

// serveOn starts srv on a loopback listener via Serve and returns the address.
func serveOn(t *testing.T, srv *http.Server, certFile, keyFile string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := Serve(srv, ln, certFile, keyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
}

// TestServeTLSEndToEnd performs a real handshake against the configured
// cert/key and asserts the negotiated protocol is at least TLS 1.2.
func TestServeTLSEndToEnd(t *testing.T) {
	certFile, keyFile, cert := selfSignedCert(t, t.TempDir())
	addr := serveOn(t, &http.Server{Handler: okHandler()}, certFile, keyFile)

	pool := x509.NewCertPool()
	pool.AddCert(cert)
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}

	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("https GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
	if resp.TLS == nil {
		t.Fatal("response carried no TLS connection state")
	}
	if resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("negotiated TLS version %x, want >= TLS 1.2", resp.TLS.Version)
	}
}

// TestServePlainHTTPWhenNoCert asserts the no-cert path serves plain HTTP.
func TestServePlainHTTPWhenNoCert(t *testing.T) {
	addr := serveOn(t, &http.Server{Handler: okHandler()}, "", "")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("http GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// TestServeRejectsHalfConfiguredPair asserts Serve errors out rather than
// silently downgrading to plain HTTP.
func TestServeRejectsHalfConfiguredPair(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if err := Serve(&http.Server{Handler: okHandler()}, ln, "cert.pem", ""); err == nil {
		t.Fatal("expected error for cert without key")
	}
}

package httpx

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
)

// ValidateTLSFlags checks the cert/key flag pair for the HTTPS listeners:
// either both must be set (serve TLS) or both empty (serve plain HTTP).
// A half-configured pair is a startup error, never a silent HTTP fallback.
func ValidateTLSFlags(certFile, keyFile string) error {
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("-tls-cert and -tls-key must be set together (got cert=%q key=%q)", certFile, keyFile)
	}
	return nil
}

// ListenAndServe serves srv on srv.Addr — over HTTPS when certFile and keyFile
// are both set, over plain HTTP when both are empty. See Serve for the TLS
// parameters applied.
func ListenAndServe(srv *http.Server, certFile, keyFile string) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	return Serve(srv, ln, certFile, keyFile)
}

// Serve serves srv on ln. When certFile and keyFile are set it serves TLS with
// a minimum protocol version of TLS 1.2 (Go's defaults cover the cipher-suite
// choices); otherwise it serves plain HTTP. The certificate files are read at
// startup — rotate certificates by restarting (or SIGHUP-supervising) the
// process.
//
// Callers should have validated the pair with ValidateTLSFlags; a half-set
// pair here is also rejected rather than silently downgraded.
func Serve(srv *http.Server, ln net.Listener, certFile, keyFile string) error {
	if err := ValidateTLSFlags(certFile, keyFile); err != nil {
		return err
	}
	if certFile == "" {
		return srv.Serve(ln)
	}
	if srv.TLSConfig == nil {
		srv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return srv.ServeTLS(ln, certFile, keyFile)
}

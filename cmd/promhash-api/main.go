package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/api"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/httpx"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-api: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var addr, neoURL, neoUser, neoPass, tokenFile, tlsCert, tlsKey string
	var insecureNoAuth bool
	flag.StringVar(&addr, "addr", "127.0.0.1:8080", "")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&tokenFile, "token-file", "", "file with one API bearer token per line (# comments allowed); alternative to PROMHASH_API_TOKENS")
	flag.BoolVar(&insecureNoAuth, "insecure-no-auth", false, "serve the data endpoints WITHOUT authentication (explicit opt-out; dev only)")
	flag.StringVar(&tlsCert, "tls-cert", "", "TLS certificate file; with -tls-key, serve HTTPS (TLS 1.2+)")
	flag.StringVar(&tlsKey, "tls-key", "", "TLS private-key file; must be set together with -tls-cert")
	flag.Parse()

	if err := httpx.ValidateTLSFlags(tlsCert, tlsKey); err != nil {
		return err
	}

	// Prefer the env var when the flag is empty (SHARED CONTRACTS): never
	// require the secret to be passed on the command line.
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}

	// Fail closed: tokens are required unless the operator explicitly opts out.
	tokens, err := loadTokens(tokenFile, os.Getenv("PROMHASH_API_TOKENS"))
	if err != nil {
		return err
	}
	if len(tokens) == 0 && !insecureNoAuth {
		return errors.New("no API tokens configured: set PROMHASH_API_TOKENS or -token-file, or pass -insecure-no-auth to explicitly run unauthenticated")
	}
	if insecureNoAuth && len(tokens) > 0 {
		return errors.New("-insecure-no-auth conflicts with configured tokens: choose one")
	}

	// Install graceful-shutdown signal handling before opening resources so a
	// signal during startup is honored.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		return err
	}
	// Use a fresh context for Close so the signal-cancelled ctx doesn't prevent
	// the driver from flushing its connections on shutdown.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	defer drv.Close(closeCtx)

	if err := drv.VerifyConnectivity(ctx); err != nil {
		return err
	}

	var handler http.Handler = api.NewServer(graph.New(drv, "neo4j")).Mux()
	if len(tokens) > 0 {
		handler = api.WithAuth(handler, tokens)
	} else {
		log.Print("WARNING: promhash-api running UNAUTHENTICATED (-insecure-no-auth)")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Route ListenAndServe errors out of the goroutine instead of log.Fatal-ing,
	// so the deferred Close and graceful shutdown path are never skipped.
	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if tlsCert != "" {
			scheme = "https"
		}
		log.Printf("promhash-api listening on %s (%s)", addr, scheme)
		if err := httpx.ListenAndServe(srv, tlsCert, tlsKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done(): // signal-driven shutdown
	}

	log.Print("promhash-api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	return nil
}

// loadTokens assembles the bearer-token set from a token file (one token per
// line; blank lines and #-comments ignored) and the PROMHASH_API_TOKENS env
// value (comma-separated). Both sources may be used together; duplicates are
// collapsed. Whitespace around tokens is trimmed.
func loadTokens(tokenFile, envTokens string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	add := func(tok string) {
		tok = strings.TrimSpace(tok)
		if tok == "" {
			return
		}
		if _, dup := seen[tok]; dup {
			return
		}
		seen[tok] = struct{}{}
		out = append(out, tok)
	}

	if tokenFile != "" {
		data, err := os.ReadFile(tokenFile)
		if err != nil {
			return nil, fmt.Errorf("read -token-file: %w", err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if i := strings.IndexByte(line, '#'); i >= 0 {
				line = line[:i]
			}
			add(line)
		}
	}
	for _, tok := range strings.Split(envTokens, ",") {
		add(tok)
	}
	return out, nil
}

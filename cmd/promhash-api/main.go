package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/api"
	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-api: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var addr, neoURL, neoUser, neoPass string
	flag.StringVar(&addr, "addr", "127.0.0.1:8080", "")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.Parse()

	// Prefer the env var when the flag is empty (SHARED CONTRACTS): never
	// require the secret to be passed on the command line.
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
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

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(graph.New(drv, "neo4j")).Mux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Route ListenAndServe errors out of the goroutine instead of log.Fatal-ing,
	// so the deferred Close and graceful shutdown path are never skipped.
	errCh := make(chan error, 1)
	go func() {
		log.Printf("promhash-api listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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

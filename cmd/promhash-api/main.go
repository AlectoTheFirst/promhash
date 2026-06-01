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
		log.Fatal(err)
	}
	defer drv.Close(ctx)
	if err := drv.VerifyConnectivity(ctx); err != nil {
		log.Fatalf("neo4j connectivity: %v", err)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           api.NewServer(graph.New(drv, "neo4j")).Mux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("promhash-api listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()
	log.Print("promhash-api shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("shutdown: %v", err)
	}
}

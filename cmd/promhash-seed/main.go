package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/AlectoTheFirst/promhash/internal/servicenow"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-seed: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var neoURL, neoUser, neoPass, neoDB, snURL, snUser, snPass string
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&neoDB, "neo4j-db", "neo4j", "Neo4j database name")
	flag.StringVar(&snURL, "servicenow", "", "")
	flag.StringVar(&snUser, "servicenow-user", "", "")
	flag.StringVar(&snPass, "servicenow-pass", "", "")
	flag.Parse()
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}
	if snPass == "" {
		snPass = os.Getenv("SERVICENOW_PASS")
	}
	ctx := context.Background()
	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		return err
	}
	defer drv.Close(ctx)
	if err := drv.VerifyConnectivity(ctx); err != nil {
		return err
	}
	r := graph.New(drv, neoDB)
	if err := r.EnsureConstraints(ctx); err != nil {
		return fmt.Errorf("ensure constraints: %w", err)
	}
	snctx, sncancel := context.WithTimeout(ctx, 60*time.Second)
	apps, err := servicenow.New(snURL, snUser, snPass).Applications(snctx)
	sncancel()
	if err != nil {
		return err
	}
	var failed bool
	seeded := 0
	for _, a := range apps {
		if err := r.UpsertAppSeed(ctx, phash.Hash(phash.KindApp, a.Name), a.Name,
			phash.Hash(phash.KindAppSvc, a.Service), a.Service, a.SysID); err != nil {
			log.Printf("upsert app %q: %v", a.Name, err)
			failed = true
			continue
		}
		seeded++
	}
	log.Printf("seeded %d applications", seeded)
	if failed {
		return errors.New("one or more applications failed to seed")
	}
	return nil
}

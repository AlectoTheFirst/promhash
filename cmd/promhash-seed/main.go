package main

import (
	"context"
	"flag"
	"log"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/starkweb/promhash/internal/graph"
	"github.com/starkweb/promhash/internal/phash"
	"github.com/starkweb/promhash/internal/servicenow"
)

func main() {
	var neoURL, neoUser, neoPass, snURL, snUser, snPass string
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&snURL, "servicenow", "", "")
	flag.StringVar(&snUser, "servicenow-user", "", "")
	flag.StringVar(&snPass, "servicenow-pass", "", "")
	flag.Parse()
	ctx := context.Background()
	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		log.Fatal(err)
	}
	defer drv.Close(ctx)
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	apps, err := servicenow.New(snURL, snUser, snPass).Applications(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, a := range apps {
		_ = r.UpsertAppSeed(ctx, phash.Hash(phash.KindApp, a.Name), a.Name,
			phash.Hash(phash.KindAppSvc, a.Service), a.Service, a.SysID)
	}
	log.Printf("seeded %d applications", len(apps))
}

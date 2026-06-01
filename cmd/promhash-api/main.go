package main

import (
	"context"
	"flag"
	"log"
	"net/http"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/starkweb/promhash/internal/api"
	"github.com/starkweb/promhash/internal/graph"
)

func main() {
	var addr, neoURL, neoUser, neoPass string
	flag.StringVar(&addr, "addr", ":8080", "")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.Parse()
	ctx := context.Background()
	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		log.Fatal(err)
	}
	defer drv.Close(ctx)
	srv := api.NewServer(graph.New(drv, "neo4j"))
	log.Printf("promhash-api listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, srv.Mux()))
}

package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/nautobot"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

func main() {
	var promURL, neoURL, neoUser, neoPass, nbURL, nbToken, vendor string
	flag.StringVar(&promURL, "prometheus", "http://localhost:9090", "Prometheus base URL")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "Neo4j bolt URL")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&nbURL, "nautobot", "", "Nautobot base URL")
	flag.StringVar(&nbToken, "nautobot-token", "", "")
	flag.StringVar(&vendor, "vendor", "cisco", "default vendor for canonicalization")
	flag.Parse()
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}
	if nbToken == "" {
		nbToken = os.Getenv("NAUTOBOT_TOKEN")
	}
	ctx := context.Background()
	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		log.Fatal(err)
	}
	defer drv.Close(ctx)
	if err := drv.VerifyConnectivity(ctx); err != nil {
		log.Fatal(err)
	}
	r := graph.New(drv, "neo4j")
	if err := r.EnsureConstraints(ctx); err != nil {
		log.Fatal(err)
	}
	pc, err := promclient.New(promURL)
	if err != nil {
		log.Fatal(err)
	}
	rows, err := pc.HarvestInterfaces(ctx)
	if err != nil {
		log.Fatal(err)
	}
	devMap := map[string]string{}
	if nbURL != "" {
		if devMap, err = nautobot.New(nbURL, nbToken).DeviceInstanceMap(ctx); err != nil {
			log.Fatal(err)
		}
	}
	inv := map[string]string{}
	for dev, ip := range devMap {
		inv[ip] = dev
	} // instance(ip) -> device
	if err := catalog.Sync(ctx, r, rows, inv, vendor); err != nil {
		log.Fatal(err)
	}
	log.Printf("catalog sync: %d interfaces", len(rows))
}

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/nautobot"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-catalog: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var promURL, neoURL, neoUser, neoPass, nbURL, nbToken, vendor, deviceLabel string
	var timeout time.Duration
	flag.StringVar(&promURL, "prometheus", "http://localhost:9090", "Prometheus base URL")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "Neo4j bolt URL")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&nbURL, "nautobot", "", "Nautobot base URL")
	flag.StringVar(&nbToken, "nautobot-token", "", "")
	flag.StringVar(&vendor, "vendor", "cisco", "default vendor for canonicalization")
	flag.StringVar(&deviceLabel, "device-label", "hostname", "series label carrying the human device name (set by file_sd target labels); empty to disable")
	flag.DurationVar(&timeout, "timeout", 60*time.Second, "per-call deadline for upstream HTTP requests")
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
		return err
	}
	defer drv.Close(ctx)
	if err := drv.VerifyConnectivity(ctx); err != nil {
		return err
	}
	r := graph.New(drv, "neo4j")
	if err := r.EnsureConstraints(ctx); err != nil {
		return err
	}
	pc, err := promclient.NewWithTimeout(promURL, timeout)
	if err != nil {
		return err
	}
	hctx, hcancel := context.WithTimeout(ctx, timeout)
	rows, skipped, err := pc.HarvestInterfaces(hctx, deviceLabel)
	hcancel()
	if err != nil {
		return err
	}
	var devMap map[string]string
	if nbURL != "" {
		nbctx, nbcancel := context.WithTimeout(ctx, timeout)
		devMap, err = nautobot.New(nbURL, nbToken).DeviceInstanceMap(nbctx)
		nbcancel()
		if err != nil {
			return err
		}
	}
	inv := map[string]string{}
	for dev, ip := range devMap {
		inv[ip] = dev
	} // instance(ip) -> device
	if err := catalog.Sync(ctx, r, rows, inv, vendor); err != nil {
		return err
	}
	ifaces, err := r.ListAllInterfaces(ctx)
	if err != nil {
		return err
	}
	oldest := catalog.OldestObservedAt(ifaces)
	oldestStr := "n/a"
	if !oldest.IsZero() {
		oldestStr = oldest.Format(time.RFC3339)
	}
	log.Printf("catalog sync: %d interfaces (%d skipped), oldest observedAt %s", len(rows), skipped, oldestStr)
	return nil
}

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/starkweb/promhash/internal/enrich"
	"github.com/starkweb/promhash/internal/graph"
	"github.com/starkweb/promhash/internal/phash"
)

func main() {
	var neoURL, neoUser, neoPass, outDir, allowlist string
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&outDir, "out", "gitops/enrichment", "output dir for artifacts")
	flag.StringVar(&allowlist, "apps", "", "comma-separated curated app names")
	flag.Parse()
	ctx := context.Background()
	drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
	if err != nil {
		log.Fatal(err)
	}
	defer drv.Close(ctx)
	r := graph.New(drv, "neo4j")
	for _, app := range strings.Split(allowlist, ",") {
		app = strings.TrimSpace(app)
		if app == "" {
			continue
		}
		hops, err := r.AppPath(ctx, phash.Hash(phash.KindApp, app), time.Now())
		if err != nil {
			log.Fatal(err)
		}
		if len(hops) == 0 {
			log.Printf("WARN app %q has no known path; skipping", app)
			continue
		}
		svc, _ := r.AppServiceName(ctx, app)
		dir := filepath.Join(outDir, app)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "federate.match"), []byte(enrich.FederationMatch(hops)+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "rules.yaml"), []byte(enrich.RuleGroup(app, svc, hops)), 0o644)
		log.Printf("app %q: %d hops -> %s", app, len(hops), dir)
	}
}

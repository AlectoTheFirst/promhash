package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/enrich"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-enrich: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		neoURL, neoUser, neoPass string
		outDir, allowlist        string
		mainProm                 string
		mappingTarget            string
		mappingPath              string
		remoteWriteURL           string
		tenantLabel              string
		joinKeyStr               string
		pruneLegacy              bool
	)
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&outDir, "out", "gitops/enrichment", "output dir for artifacts")
	flag.StringVar(&allowlist, "apps", "", "comma-separated curated app names")
	flag.StringVar(&mainProm, "main-prom", "", "scrape target host:port for the shared evaluator (ScrapeTarget)")
	flag.StringVar(&mappingTarget, "mapping-target", "", "host:port serving the rendered mapping.prom for the evaluator's mapping scrape job")
	flag.StringVar(&mappingPath, "mapping-path", "", "HTTP path of the mapping exposition on -mapping-target (default /mapping.prom)")
	flag.StringVar(&remoteWriteURL, "remote-write-url", "", "URL of the remote_write receiver")
	flag.StringVar(&tenantLabel, "tenant-label", "", "value stamped as global.external_labels.tenant")
	flag.StringVar(&joinKeyStr, "join-key", "composite", "join key for path-health rules: composite (default) or ifname")
	flag.BoolVar(&pruneLegacy, "prune-legacy", false, "remove stale per-app legacy artifacts (federate.match, rules.yaml, scrape.yaml) from outDir")
	flag.Parse()

	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}

	if err := validateRequiredFlags(mainProm, mappingTarget, remoteWriteURL, tenantLabel); err != nil {
		return err
	}

	jk, err := parseJoinKey(joinKeyStr)
	if err != nil {
		return err
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

	apps := make(map[string][]graph.Hop)
	svcNames := make(map[string]string)

	for _, app := range strings.Split(allowlist, ",") {
		app = strings.TrimSpace(app)
		if app == "" {
			continue
		}
		hops, err := r.AppPath(ctx, phash.Hash(phash.KindApp, app), time.Now())
		if err != nil {
			return err
		}
		if len(hops) == 0 {
			log.Printf("WARN app %q has no known path; skipping", app)
			continue
		}
		svc, _ := r.AppServiceName(ctx, app)
		apps[app] = hops
		svcNames[app] = svc
		log.Printf("app %q: %d hops collected", app, len(hops))
	}

	opts := enrich.EvaluatorOpts{
		ScrapeTarget:       mainProm,
		MappingTarget:      mappingTarget,
		MappingMetricsPath: mappingPath,
		RemoteWriteURL:     remoteWriteURL,
		TenantLabel:        tenantLabel,
		JoinKey:            jk,
	}

	if err := writeSharedArtifacts(outDir, apps, svcNames, opts); err != nil {
		return err
	}

	if pruneLegacy {
		if err := pruneLegacyArtifacts(outDir, apps); err != nil {
			return err
		}
	}

	return nil
}

// validateRequiredFlags rejects an invocation missing any flag without which
// the generated evaluator.yaml would be silently broken: no counter scrape, no
// mapping ingestion (rules join against nothing), no remote_write destination,
// or no tenant identity.
func validateRequiredFlags(mainProm, mappingTarget, remoteWriteURL, tenantLabel string) error {
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"-main-prom", mainProm},
		{"-mapping-target", mappingTarget},
		{"-remote-write-url", remoteWriteURL},
		{"-tenant-label", tenantLabel},
	} {
		if strings.TrimSpace(f.val) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}
	return nil
}

// parseJoinKey maps the -join-key flag value to the enrich.JoinKey constant.
// It rejects unknown values with a descriptive error.
func parseJoinKey(s string) (enrich.JoinKey, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "composite":
		return enrich.JoinByComposite, nil
	case "ifname":
		return enrich.JoinByIfName, nil
	default:
		return 0, fmt.Errorf("-join-key %q: unknown value; must be \"composite\" or \"ifname\"", s)
	}
}

// writeSharedArtifacts builds the combined mapping points for all apps and
// writes the three shared artifacts under dir/_shared/:
//
//   - mapping.prom       — promhash_interface_app{…}=1 exposition text
//   - path-health.rules.yaml — static path-health recording-rule group
//   - evaluator.yaml         — shared Prometheus-agent config
//
// It does NOT require a live Neo4j; all inputs are pre-resolved by the caller.
func writeSharedArtifacts(dir string, apps map[string][]graph.Hop, svcNames map[string]string, opts enrich.EvaluatorOpts) error {
	sharedDir := filepath.Join(dir, "_shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", sharedDir, err)
	}

	// Build combined mapping points across all apps.
	var allPoints []enrich.MappingPoint
	for app, hops := range apps {
		svc := svcNames[app]
		pts := enrich.MappingSeries(app, svc, hops, opts.JoinKey)
		allPoints = append(allPoints, pts...)
	}

	mappingText := enrich.RenderMappingSeries(allPoints)
	if err := os.WriteFile(filepath.Join(sharedDir, "mapping.prom"), []byte(mappingText), 0o644); err != nil {
		return fmt.Errorf("write mapping.prom: %w", err)
	}

	rulesYAML := enrich.PathHealthRules(opts.JoinKey)
	if err := os.WriteFile(filepath.Join(sharedDir, "path-health.rules.yaml"), []byte(rulesYAML), 0o644); err != nil {
		return fmt.Errorf("write path-health.rules.yaml: %w", err)
	}

	evaluatorYAML := enrich.SharedEvaluatorConfig(opts)
	if err := os.WriteFile(filepath.Join(sharedDir, "evaluator.yaml"), []byte(evaluatorYAML), 0o644); err != nil {
		return fmt.Errorf("write evaluator.yaml: %w", err)
	}

	log.Printf("_shared/: wrote mapping.prom (%d points), path-health.rules.yaml, evaluator.yaml", len(allPoints))
	return nil
}

// pruneLegacyArtifacts removes the three legacy per-app files
// (federate.match, rules.yaml, scrape.yaml) from each app's subdirectory
// under dir. If removing all three leaves the app directory empty, the
// directory itself is removed. The _shared/ subdirectory is never touched.
//
// scrape.yaml is included for defensive coverage: this binary never writes it,
// but earlier tooling (TenantScrapeConfig) may have left it behind in app dirs.
func pruneLegacyArtifacts(dir string, apps map[string][]graph.Hop) error {
	legacyFiles := []string{"federate.match", "rules.yaml", "scrape.yaml"}
	for app := range apps {
		// Guard against path-traversal: only touch direct children of dir.
		// An app name containing a separator or that differs from its own
		// Base (e.g. "../evil") would resolve outside dir — skip it.
		if app == "" || app == "." || app == ".." ||
			strings.ContainsRune(app, '/') ||
			strings.ContainsRune(app, os.PathSeparator) ||
			filepath.Base(app) != app {
			log.Printf("prune-legacy: skipping unsafe app name %q", app)
			continue
		}
		appDir := filepath.Join(dir, app)
		for _, f := range legacyFiles {
			p := filepath.Join(appDir, f)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("prune-legacy: remove %s: %w", p, err)
			}
		}
		// Remove the app dir if it is now empty.
		entries, err := os.ReadDir(appDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("prune-legacy: readdir %s: %w", appDir, err)
		}
		if len(entries) == 0 {
			if err := os.Remove(appDir); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("prune-legacy: rmdir %s: %w", appDir, err)
			}
		}
	}
	return nil
}

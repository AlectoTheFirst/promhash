package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/enrich"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
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
		apiTarget                string
		mappingPath              string
		apiTokenFile             string
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
	flag.StringVar(&apiTarget, "promhash-api", "", "host:port of the promhash-api serving the live GET /mapping.prom exposition")
	flag.StringVar(&mappingPath, "mapping-path", "", "HTTP path of the mapping exposition on -promhash-api (default /mapping.prom)")
	flag.StringVar(&apiTokenFile, "api-token-file", "", "path (on the promhash Prometheus host) of a file holding a Bearer token accepted by promhash-api; rendered as the scrape job's credentials_file")
	flag.StringVar(&remoteWriteURL, "remote-write-url", "", "URL of the onward remote_write receiver (long-term storage)")
	flag.StringVar(&tenantLabel, "tenant-label", "", "value stamped as global.external_labels.tenant")
	flag.StringVar(&joinKeyStr, "join-key", "ifname", "join key for path-health rules: ifname (default) or composite (requires the counter-scraping Prometheus to synthesize the iface label)")
	flag.BoolVar(&pruneLegacy, "prune-legacy", false, "remove stale per-app legacy artifacts (federate.match, rules.yaml, scrape.yaml) from outDir")
	flag.Parse()

	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}

	if err := validateRequiredFlags(apiTarget, remoteWriteURL, tenantLabel); err != nil {
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
		apps[app] = hops
		log.Printf("app %q: %d hops collected", app, len(hops))
	}

	curated := make([]string, 0, len(apps))
	for app := range apps {
		curated = append(curated, app)
	}
	sort.Strings(curated)

	opts := enrich.EvaluatorOpts{
		MappingTarget:      apiTarget,
		MappingMetricsPath: mappingPath,
		CuratedApps:        curated,
		APITokenFile:       apiTokenFile,
		RemoteWriteURL:     remoteWriteURL,
		TenantLabel:        tenantLabel,
	}

	if err := writeSharedArtifacts(outDir, jk, opts); err != nil {
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
// the generated evaluator.yaml would be silently broken: no mapping ingestion
// (rules join against nothing), no remote_write destination, or no tenant
// identity. The raw counters need no flag; they arrive via remote_write from
// the main Prometheus, configured out of band.
func validateRequiredFlags(apiTarget, remoteWriteURL, tenantLabel string) error {
	var missing []string
	for _, f := range []struct{ name, val string }{
		{"-promhash-api", apiTarget},
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

// writeSharedArtifacts writes the three shared configuration artifacts under
// dir/_shared/:
//
//   - path-health.rules.yaml  — static path-health recording-rule group
//   - path-health.alerts.yaml — pipeline + path alerting rules
//   - evaluator.yaml          — promhash Prometheus config (receiver mode)
//
// The mapping series is NOT written as a file: it is generated data, served
// live by promhash-api at GET /mapping.prom and scraped via the job that
// evaluator.yaml carries. Only reviewable configuration travels through
// GitOps.
//
// It does NOT require a live Neo4j; all inputs are pre-resolved by the caller.
func writeSharedArtifacts(dir string, jk enrich.JoinKey, opts enrich.EvaluatorOpts) error {
	sharedDir := filepath.Join(dir, "_shared")
	if err := os.MkdirAll(sharedDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", sharedDir, err)
	}

	rulesYAML := enrich.PathHealthRules(jk)
	if err := os.WriteFile(filepath.Join(sharedDir, enrich.RulesFileName), []byte(rulesYAML), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", enrich.RulesFileName, err)
	}

	alertsYAML := enrich.PathHealthAlerts(jk)
	if err := os.WriteFile(filepath.Join(sharedDir, enrich.AlertsFileName), []byte(alertsYAML), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", enrich.AlertsFileName, err)
	}

	evaluatorYAML := enrich.SharedEvaluatorConfig(opts)
	if err := os.WriteFile(filepath.Join(sharedDir, "evaluator.yaml"), []byte(evaluatorYAML), 0o644); err != nil {
		return fmt.Errorf("write evaluator.yaml: %w", err)
	}

	log.Printf("_shared/: wrote %s, %s, evaluator.yaml (curated apps: %s)", enrich.RulesFileName, enrich.AlertsFileName, strings.Join(opts.CuratedApps, ","))
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

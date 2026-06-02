package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/declare"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-loader: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var dir, neoURL, neoUser, neoPass, sha, commitTimeStr string
	var validateOnly, allowEmpty bool
	flag.StringVar(&dir, "dir", "declared", "directory of *.yaml declarations")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&sha, "source", "manual", "git sha for provenance")
	flag.StringVar(&commitTimeStr, "commit-time", "", "RFC3339 git commit time to use as validFrom base (overrides GIT_COMMIT_TIME env)")
	flag.BoolVar(&validateOnly, "validate-only", false, "CI gate: validate, do not write")
	flag.BoolVar(&allowEmpty, "allow-empty", false, "allow zero *.yaml files in -dir (opt-in; otherwise an empty or misconfigured dir is a fatal error)")
	flag.Parse()
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
	}
	if commitTimeStr == "" {
		commitTimeStr = os.Getenv("GIT_COMMIT_TIME")
	}

	// Determine the validFrom base: prefer commit time (from flag or env) so
	// reloads are anchored to the git history rather than wall clock; fall back
	// to time.Now() for manual / legacy runs.
	var now time.Time
	if commitTimeStr != "" {
		parsed, err := time.Parse(time.RFC3339, commitTimeStr)
		if err != nil {
			return fmt.Errorf("invalid commit-time %q: %w", commitTimeStr, err)
		}
		now = parsed.UTC()
	} else {
		now = time.Now().UTC()
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
	cat, err := r.ListAllInterfaces(ctx)
	if err != nil {
		return err
	}
	res := catalog.NewResolver(cat)

	// Glob guard: capture error and treat zero matches as fatal unless
	// -allow-empty is set. A misconfigured -dir must be loud, not silent.
	files, globErr := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if globErr != nil {
		return fmt.Errorf("glob %s: %w", dir, globErr)
	}
	if len(files) == 0 && !allowEmpty {
		return fmt.Errorf("%s: no *.yaml declarations found", dir)
	}

	var failed bool
	present := make(map[string]bool)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			log.Printf("%s: read: %v", f, err)
			failed = true
			continue
		}
		a, err := declare.Parse(b)
		if err != nil {
			log.Printf("%s: parse: %v", f, err)
			failed = true
			continue
		}
		if errs := declare.Validate(a, res); len(errs) != 0 {
			for _, e := range errs {
				log.Printf("%s: %v", f, e)
			}
			failed = true
			continue
		}
		if !validateOnly {
			if err := declare.Load(ctx, r, a, res, sha, now); err != nil {
				log.Printf("%s: load: %v", f, err)
				failed = true
				continue
			}
		}
		// Only mark present after parse + validate + load all succeeded.
		present[phash.Hash(phash.KindApp, a.App)] = true
	}
	if failed {
		return errors.New("one or more declarations failed")
	}

	// Reconcile / tombstone pass: close validity for apps whose declaration
	// was removed. Guards ensure we never retract during validate-only runs,
	// partial (failed) batches, or when the glob was empty without -allow-empty.
	if !validateOnly && (len(files) > 0 || allowEmpty) {
		if n, err := reconcile(ctx, r, present, now); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		} else if n > 0 {
			log.Printf("retracted %d decommissioned app(s)", n)
		}
	}

	log.Printf("processed %d declarations (validateOnly=%v)", len(files), validateOnly)
	return nil
}

// reconcile closes validity for every currently-open declared app whose phash
// is absent from present. It returns the number of apps retracted.
//
// Safety gates (must all be enforced by the caller before invoking):
//   - !validateOnly  — never tombstone in CI-gate mode
//   - !failed        — never tombstone when the batch had parse/validate/load errors
//   - non-empty glob OR -allow-empty — never tombstone from a misconfigured dir
func reconcile(ctx context.Context, r *graph.Repo, present map[string]bool, now time.Time) (int, error) {
	open, err := r.ListOpenDeclaredApps(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	for _, p := range open {
		if present[p] {
			continue
		}
		if err := r.CloseAppValidity(ctx, p, now); err != nil {
			return n, fmt.Errorf("close %s: %w", p, err)
		}
		log.Printf("retracted app %s", p)
		n++
	}
	return n, nil
}

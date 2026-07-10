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

	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/declare"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-loader: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var dir, neoURL, neoUser, neoPass, neoDB, sha, commitTimeStr string
	var validateOnly, allowEmpty bool
	flag.StringVar(&dir, "dir", "declared", "directory of *.yaml declarations")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&neoDB, "neo4j-db", "neo4j", "Neo4j database name")
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
	r := graph.New(drv, neoDB)
	// The loader creates Application/ApplicationService/... nodes, so the
	// uniqueness constraints must exist before any write. Skipped in
	// validate-only mode, which must not write anything (including schema).
	if !validateOnly {
		if err := r.EnsureConstraints(ctx); err != nil {
			return fmt.Errorf("ensure constraints: %w", err)
		}
	}
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
	declaredBy := make(map[string]string)
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
		if prevFile, dup := registerApp(declaredBy, a.App, f); dup {
			log.Printf("%s: app %q already declared in %s (one declaration file per app)", f, a.App, prevFile)
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
	// was removed. shouldReconcile enforces all safety gates before we act.
	if shouldReconcile(validateOnly, failed, len(files), allowEmpty) {
		if closed, err := reconcileRetractions(ctx, r, present, now); err != nil {
			return fmt.Errorf("reconcile: %w", err)
		} else if len(closed) > 0 {
			log.Printf("retracted %d decommissioned app(s)", len(closed))
		}
	}

	log.Printf("processed %d declarations (validateOnly=%v)", len(files), validateOnly)
	return nil
}

// registerApp records which file declares the app, keyed by the app's phash
// (case/whitespace-insensitive, matching graph identity). It returns the
// previously-registering file and true when another file in the batch already
// declared the same app: two files declaring one app would silently
// last-write-win in the graph, so the batch must fail loudly instead.
func registerApp(seen map[string]string, app, file string) (prevFile string, dup bool) {
	key := phash.Hash(phash.KindApp, app)
	if prev, ok := seen[key]; ok {
		return prev, true
	}
	seen[key] = file
	return "", false
}

// appRetractor is the minimal seam needed by reconcileRetractions. *graph.Repo
// satisfies this interface; tests inject a fake without requiring Neo4j.
type appRetractor interface {
	ListOpenDeclaredApps(ctx context.Context) ([]string, error)
	CloseAppValidity(ctx context.Context, appPHash string, at time.Time) error
}

// shouldReconcile returns true only when all safety gates pass and it is safe
// to tombstone decommissioned apps.
//
//   - validateOnly: CI-gate mode — never tombstone
//   - failed: batch had parse/validate/load errors — never tombstone a partial batch
//   - fileCount: number of declaration files found by glob
//   - allowEmpty: caller explicitly opted in to an empty dir being valid
//
// The reconcile pass requires either a non-empty glob OR an explicit -allow-empty
// flag; without one of those we might mass-retract everything due to a
// misconfigured -dir.
func shouldReconcile(validateOnly, failed bool, fileCount int, allowEmpty bool) bool {
	return !validateOnly && !failed && (fileCount > 0 || allowEmpty)
}

// reconcileRetractions closes validity for every currently-open declared app
// whose phash is absent from present. It returns the slice of phashes that
// were retracted. On the first CloseAppValidity error it stops and returns
// what was closed so far alongside the error (fail-fast).
func reconcileRetractions(ctx context.Context, r appRetractor, present map[string]bool, now time.Time) ([]string, error) {
	open, err := r.ListOpenDeclaredApps(ctx)
	if err != nil {
		return nil, err
	}
	var closed []string
	for _, p := range open {
		if present[p] {
			continue
		}
		if err := r.CloseAppValidity(ctx, p, now); err != nil {
			return closed, fmt.Errorf("close %s: %w", p, err)
		}
		log.Printf("retracted app %s", p)
		closed = append(closed, p)
	}
	return closed, nil
}

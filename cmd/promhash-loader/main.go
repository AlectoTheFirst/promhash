package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/declare"
	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-loader: %v", err)
		os.Exit(1)
	}
}

func run() error {
	var dir, neoURL, neoUser, neoPass, sha string
	var validateOnly bool
	flag.StringVar(&dir, "dir", "declared", "directory of *.yaml declarations")
	flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
	flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
	flag.StringVar(&neoPass, "neo4j-pass", "", "")
	flag.StringVar(&sha, "source", "manual", "git sha for provenance")
	flag.BoolVar(&validateOnly, "validate-only", false, "CI gate: validate, do not write")
	flag.Parse()
	if neoPass == "" {
		neoPass = os.Getenv("NEO4J_PASS")
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
	files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	now := time.Now().UTC()
	var failed bool
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
			}
		}
	}
	if failed {
		return errors.New("one or more declarations failed")
	}
	log.Printf("processed %d declarations (validateOnly=%v)", len(files), validateOnly)
	return nil
}

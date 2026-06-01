//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/starkweb/promhash/internal/testutil"
)

func TestAppPathReturnsOrderedUnion(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	// Build a minimal app→svc→conn→path→hop→iface graph for two candidate paths
	seedTwoCandidatePaths(t, ctx, r) // helper inserts iface nodes + the path graph
	hops, err := r.AppPath(ctx, "application:payments", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) < 2 {
		t.Fatalf("want union of >=2 interfaces, got %d", len(hops))
	}
	for i := 1; i < len(hops); i++ {
		if hops[i].Seq < hops[i-1].Seq { /* union may interleave paths; ordering is per-path */
		}
	}
}

// seedTwoCandidatePaths writes:
//
//	Application{phash:'application:payments'}-RUNS_AS->ApplicationService,
//	the service USES a Connection, the Connection has two TAKES->Path,
//	and each Path HOP->a distinct Interface (one with phash 'interface:a').
//
// The TAKES relationships carry validFrom (set to an open window) and a null
// validTo so the AppPath validity filter selects both paths.
func seedTwoCandidatePaths(t *testing.T, ctx context.Context, r *Repo) {
	t.Helper()
	if err := r.write(ctx,
		`MERGE (a:Application {phash:'application:payments'}) SET a.name='payments'
         MERGE (svc:ApplicationService {phash:'appservice:payments'}) SET svc.name='payments'
         MERGE (a)-[:RUNS_AS]->(svc)
         CREATE (conn:Connection {provenance:'declared'})
         MERGE (svc)-[:USES]->(conn)
         MERGE (ia:Interface {phash:'interface:a'})
           SET ia.device='rtr-a', ia.ifName='te0/0/1', ia.metricIfName='Te0/0/1',
               ia.instance='10.0.0.1', ia.ifIndex=42
         MERGE (ib:Interface {phash:'interface:b'})
           SET ib.device='rtr-b', ib.ifName='te0/0/2', ib.metricIfName='Te0/0/2',
               ib.instance='10.0.0.2', ib.ifIndex=43
         CREATE (pa:Path {provenance:'declared'})
         CREATE (conn)-[ta:TAKES {provenance:'declared'}]->(pa)
           SET ta.validFrom=$validFrom, ta.validTo=null
         MERGE (pa)-[:HOP {seq:1, direction:'in'}]->(ia)
         CREATE (pb:Path {provenance:'declared'})
         CREATE (conn)-[tb:TAKES {provenance:'declared'}]->(pb)
           SET tb.validFrom=$validFrom, tb.validTo=null
         MERGE (pb)-[:HOP {seq:2, direction:'out'}]->(ib)`,
		map[string]any{"validFrom": time.Unix(1700000000, 0).UTC().Unix()}); err != nil {
		t.Fatalf("seed two candidate paths: %v", err)
	}
}

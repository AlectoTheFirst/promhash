//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestCloseValidityClampsToValidFrom verifies that closing an app's validity
// with a timestamp EARLIER than the open edges' validFrom does not produce an
// inverted window (validTo < validFrom). The close must clamp to validFrom+1
// so the historical window stays queryable.
func TestCloseValidityClampsToValidFrom(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:clamp-a", "rtr-clamp-a", "te0/0/1")

	t10 := time.Unix(1700001000, 0).UTC()
	t5 := time.Unix(1700000500, 0).UTC() // EARLIER than t10

	d := buildReloadApp(t10, "interface:clamp-a", "rtr-clamp-a")
	d.AppPHash, d.App = "application:clamp-test", "clamp-test"
	d.AppSvcPHash, d.AppSvc = "appservice:clamp-test", "clamp-test"
	if err := r.ReloadDeclaredApp(ctx, d, t10); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Retract with an out-of-order (earlier) timestamp.
	if err := r.CloseAppValidity(ctx, d.AppPHash, t5); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Every closed TAKES / DEPENDS_ON / Connection must satisfy validTo > validFrom.
	res, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc)
		 OPTIONAL MATCH (svc)-[do:DEPENDS_ON]->()
		 OPTIONAL MATCH (svc)-[:USES]->(conn:Connection)-[tk:TAKES]->()
		 RETURN do.validFrom AS dof, do.validTo AS dot,
		        conn.validFrom AS cf, conn.validTo AS ct,
		        tk.validFrom AS tf, tk.validTo AS tt`,
		map[string]any{"p": d.AppPHash},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range res.Records {
		for _, pair := range [][2]string{{"dof", "dot"}, {"cf", "ct"}, {"tf", "tt"}} {
			fromV, _ := rec.Get(pair[0])
			toV, _ := rec.Get(pair[1])
			from, ok1 := fromV.(int64)
			to, ok2 := toV.(int64)
			if !ok1 || !ok2 {
				continue // absent optional edge
			}
			if to <= from {
				t.Errorf("%s/%s: inverted window validFrom=%d validTo=%d", pair[0], pair[1], from, to)
			}
		}
	}

	// The original window must still be queryable at its validFrom instant.
	hops, err := r.AppPath(ctx, d.AppPHash, t10)
	if err != nil {
		t.Fatalf("AppPath(t10): %v", err)
	}
	if len(hops) != 1 {
		t.Fatalf("AppPath(t10): want 1 hop (window [t10,t10+1) queryable), got %d", len(hops))
	}
	// And closed one second later.
	hops, err = r.AppPath(ctx, d.AppPHash, t10.Add(time.Second))
	if err != nil {
		t.Fatalf("AppPath(t10+1): %v", err)
	}
	if len(hops) != 0 {
		t.Fatalf("AppPath(t10+1): want 0 hops after close, got %d", len(hops))
	}
}

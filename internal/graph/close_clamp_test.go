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

// TestReloadAfterRetractionNoOverlap verifies that re-declaring an app with a
// timestamp EARLIER than its retraction does not open a window overlapping the
// closed history: no single instant may return hops from two revisions.
func TestReloadAfterRetractionNoOverlap(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:ovl-a", "rtr-ovl-a", "te0/0/1")
	seedIface(t, ctx, r, "interface:ovl-b", "rtr-ovl-b", "te0/0/2")

	t0 := time.Unix(1700000000, 0).UTC()
	t1 := time.Unix(1700001000, 0).UTC()
	t2 := time.Unix(1700002000, 0).UTC()

	d1 := buildReloadApp(t1, "interface:ovl-a", "rtr-ovl-a")
	d1.AppPHash, d1.App = "application:ovl-test", "ovl-test"
	d1.AppSvcPHash, d1.AppSvc = "appservice:ovl-test", "ovl-test"
	if err := r.ReloadDeclaredApp(ctx, d1, t1); err != nil {
		t.Fatalf("load rev1: %v", err)
	}
	if err := r.CloseAppValidity(ctx, d1.AppPHash, t2); err != nil {
		t.Fatalf("retract: %v", err)
	}

	// Re-declare with a hop change and an OUT-OF-ORDER timestamp t0 < t1 < t2.
	d2 := buildReloadApp(t0, "interface:ovl-b", "rtr-ovl-b")
	d2.AppPHash, d2.App = "application:ovl-test", "ovl-test"
	d2.AppSvcPHash, d2.AppSvc = "appservice:ovl-test", "ovl-test"
	if err := r.ReloadDeclaredApp(ctx, d2, t0); err != nil {
		t.Fatalf("reload rev2: %v", err)
	}

	// Mid-first-window: only revision 1's hop.
	hops, err := r.AppPath(ctx, d1.AppPHash, t1.Add(time.Second))
	if err != nil {
		t.Fatalf("AppPath(t1+1): %v", err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-ovl-a" {
		t.Fatalf("AppPath(t1+1): want exactly rev1 hop (rtr-ovl-a), got %+v", hops)
	}

	// Well after the retraction: only revision 2's hop.
	hops, err = r.AppPath(ctx, d1.AppPHash, t2.Add(time.Hour))
	if err != nil {
		t.Fatalf("AppPath(t2+1h): %v", err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-ovl-b" {
		t.Fatalf("AppPath(t2+1h): want exactly rev2 hop (rtr-ovl-b), got %+v", hops)
	}
}

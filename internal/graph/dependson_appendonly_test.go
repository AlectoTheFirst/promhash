//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// TestDependsOnAppendOnlyAcrossReloads asserts that the DEPENDS_ON edge is
// append-only: after a close+re-upsert cycle the original edge retains its
// closed interval [t1, t2] and a new open edge [t2, ∞) is created, giving
// exactly two edges. A second upsert at the same validFrom must be idempotent
// (no third edge).
func TestDependsOnAppendOnlyAcrossReloads(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	const dbName = "neo4j" // single source so New() and the raw assertion queries can't drift
	r := New(drv, dbName)
	_ = r.EnsureConstraints(ctx)

	// Seed the interface that the hop references.
	if err := r.UpsertInterface(ctx, Iface{
		PHash: "interface:hop1", Device: "rtr-a", IfName: "te0/0/1",
		MetricIfName: "Te0/0/1", Instance: "10.0.0.1", IfIndex: 7,
		ObservedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed interface: %v", err)
	}

	t1 := time.Unix(1700000000, 0).UTC()
	t2 := time.Unix(1700003600, 0).UTC() // t1 + 1 hour

	appPHash := "application:payments"

	buildApp := func(validFrom time.Time) DeclaredApp {
		return DeclaredApp{
			AppPHash:    appPHash,
			App:         "payments",
			AppSvcPHash: "appservice:payments",
			AppSvc:      "payments",
			Owner:       "team-payments",
			Customers:   nil,
			Source:      "declare-test",
			ValidFrom:   validFrom,
			Deps: []DeclaredDep{{
				ToAppSvc: "appservice:ledger",
				ToName:   "ledger",
				Paths: []DeclaredPath{{
					Hops: []DeclaredHop{{IfacePHash: "interface:hop1", Seq: 1, Direction: "egress"}},
				}},
			}},
		}
	}

	// Step 1: initial upsert at t1.
	if err := r.UpsertDeclaredApp(ctx, buildApp(t1)); err != nil {
		t.Fatalf("upsert t1: %v", err)
	}

	// Step 2: close at t2, then re-upsert at t2 (mirrors declare/load.go).
	if err := r.CloseAppValidity(ctx, appPHash, t2); err != nil {
		t.Fatalf("close at t2: %v", err)
	}
	if err := r.UpsertDeclaredApp(ctx, buildApp(t2)); err != nil {
		t.Fatalf("upsert t2: %v", err)
	}

	// Query ALL DEPENDS_ON edges from the service to the target, ordered by validFrom.
	res, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc)
		 MATCH (svc)-[do:DEPENDS_ON]->(target)
		 RETURN do.validFrom AS vf, do.validTo AS vt
		 ORDER BY vf`,
		map[string]any{"p": appPHash},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err != nil {
		t.Fatalf("query DEPENDS_ON edges: %v", err)
	}

	if got, want := len(res.Records), 2; got != want {
		t.Fatalf("want %d DEPENDS_ON edges, got %d (historical interval destroyed — MERGE re-matched closed edge)", want, got)
	}

	// Edge 0: the t1 edge must be closed at t2.
	rec0 := res.Records[0]
	vf0, _ := rec0.Get("vf")
	vt0, _ := rec0.Get("vt")
	if vf0i, ok := vf0.(int64); !ok || vf0i != t1.Unix() {
		t.Errorf("edge[0] validFrom: want %d, got %v", t1.Unix(), vf0)
	}
	if vt0i, ok := vt0.(int64); !ok || vt0i != t2.Unix() {
		t.Errorf("edge[0] validTo: want %d (closed at t2), got %v", t2.Unix(), vt0)
	}

	// Edge 1: the t2 edge must be open.
	rec1 := res.Records[1]
	vf1, _ := rec1.Get("vf")
	vt1, _ := rec1.Get("vt")
	if vf1i, ok := vf1.(int64); !ok || vf1i != t2.Unix() {
		t.Errorf("edge[1] validFrom: want %d, got %v", t2.Unix(), vf1)
	}
	if vt1 != nil {
		t.Errorf("edge[1] validTo: want nil (open), got %v", vt1)
	}

	// Step 3: idempotency — second upsert at the same t2 must not create a third edge.
	if err := r.UpsertDeclaredApp(ctx, buildApp(t2)); err != nil {
		t.Fatalf("idempotent re-upsert at t2: %v", err)
	}

	res2, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc)
		 MATCH (svc)-[do:DEPENDS_ON]->(target)
		 RETURN count(do) AS n`,
		map[string]any{"p": appPHash},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err != nil {
		t.Fatalf("count after idempotent upsert: %v", err)
	}
	n, _ := res2.Records[0].Get("n")
	if ni, ok := n.(int64); !ok || ni != 2 {
		t.Errorf("after idempotent re-upsert: want 2 edges, got %v", n)
	}
}

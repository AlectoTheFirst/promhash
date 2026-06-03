//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// TestReloadIdempotentUnderRetry guards the idempotency invariant for
// Connection and Path structural nodes under ExecuteWrite auto-retry.
//
// ExecuteWrite rolls back and replays the transaction function on transient
// errors (deadlock, leader switch, etc.). Any write that uses CREATE on a
// structural node that represents identity (rather than a versioned interval)
// will produce duplicates on replay. This test simulates the replay by
// invoking UpsertDeclaredApp twice with identical inputs (same DeclaredApp +
// same validFrom) and asserts that the structural node counts are UNCHANGED
// after the second run. It is correct and expected that append-only versioned
// edges (DEPENDS_ON, TAKES) may gain an interval; this test asserts specifically
// on structural NODE counts.
func TestReloadIdempotentUnderRetry(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	const dbName = "neo4j"
	r := New(drv, dbName)
	_ = r.EnsureConstraints(ctx)

	// Seed two interfaces used by two different paths of the same dependency.
	seedIface(t, ctx, r, "interface:idem-a", "rtr-idem-a", "te0/0/1")
	seedIface(t, ctx, r, "interface:idem-b", "rtr-idem-b", "te0/0/2")

	validFrom := time.Unix(1700020000, 0).UTC()

	// A declared app with one dependency that has two distinct paths.
	d := DeclaredApp{
		AppPHash:    "application:idem-test",
		App:         "idem-test",
		AppSvcPHash: "appservice:idem-test",
		AppSvc:      "idem-test",
		Owner:       "team-idem",
		Customers:   nil,
		Source:      "idem-test-source",
		ValidFrom:   validFrom,
		Deps: []DeclaredDep{{
			ToAppSvc: "appservice:idem-target",
			ToName:   "idem-target",
			Paths: []DeclaredPath{
				{Hops: []DeclaredHop{{IfacePHash: "interface:idem-a", Seq: 1, Direction: "egress"}}},
				{Hops: []DeclaredHop{{IfacePHash: "interface:idem-b", Seq: 1, Direction: "egress"}}},
			},
		}},
	}

	// First upsert: establishes the structural nodes.
	if err := r.UpsertDeclaredApp(ctx, d); err != nil {
		t.Fatalf("first UpsertDeclaredApp: %v", err)
	}

	// Count structural nodes after the first upsert.
	connCount1 := countLabel(t, ctx, drv, dbName, "Connection")
	pathCount1 := countLabel(t, ctx, drv, dbName, "Path")

	if connCount1 != 1 {
		t.Fatalf("after first upsert: want 1 Connection node, got %d", connCount1)
	}
	if pathCount1 != 2 {
		t.Fatalf("after first upsert: want 2 Path nodes (one per path), got %d", pathCount1)
	}

	// Second upsert with IDENTICAL inputs simulates an ExecuteWrite retry replay.
	// The transaction function sees the same DeclaredApp + same validFrom; if any
	// structural node uses CREATE it will be duplicated here.
	if err := r.UpsertDeclaredApp(ctx, d); err != nil {
		t.Fatalf("second UpsertDeclaredApp (retry simulation): %v", err)
	}

	// Structural node counts must be UNCHANGED.
	connCount2 := countLabel(t, ctx, drv, dbName, "Connection")
	pathCount2 := countLabel(t, ctx, drv, dbName, "Path")

	if connCount2 != connCount1 {
		t.Errorf("Connection nodes duplicated on retry: first=%d second=%d (want equal)",
			connCount1, connCount2)
	}
	if pathCount2 != pathCount1 {
		t.Errorf("Path nodes duplicated on retry: first=%d second=%d (want equal)",
			pathCount1, pathCount2)
	}

	// Relationship counts must also be unchanged (defense-in-depth: MERGE must not
	// fan out or create duplicate USES, TAKES, or HOP relationships on replay).
	usesCount1 := countRel(t, ctx, drv, dbName, "USES")
	takesCount1 := countRel(t, ctx, drv, dbName, "TAKES")
	hopCount1 := countRel(t, ctx, drv, dbName, "HOP")

	// Third upsert: another identical replay to measure rel-count stability.
	if err := r.UpsertDeclaredApp(ctx, d); err != nil {
		t.Fatalf("third UpsertDeclaredApp (rel-count retry simulation): %v", err)
	}

	usesCount2 := countRel(t, ctx, drv, dbName, "USES")
	takesCount2 := countRel(t, ctx, drv, dbName, "TAKES")
	hopCount2 := countRel(t, ctx, drv, dbName, "HOP")

	if usesCount2 != usesCount1 {
		t.Errorf("USES relationships duplicated on retry: first=%d second=%d (want equal)",
			usesCount1, usesCount2)
	}
	if takesCount2 != takesCount1 {
		t.Errorf("TAKES relationships duplicated on retry: first=%d second=%d (want equal)",
			takesCount1, takesCount2)
	}
	if hopCount2 != hopCount1 {
		t.Errorf("HOP relationships duplicated on retry: first=%d second=%d (want equal)",
			hopCount1, hopCount2)
	}

	// Sanity: AppPath must still return the correct hops after the replay.
	hops, err := r.AppPath(ctx, "application:idem-test", time.Now())
	if err != nil {
		t.Fatalf("AppPath after retry simulation: %v", err)
	}
	if len(hops) != 2 {
		t.Fatalf("AppPath: want 2 hops (one per path), got %d: %+v", len(hops), hops)
	}
}

// countLabel returns the total number of nodes with the given label.
func countLabel(t *testing.T, ctx context.Context, drv neo4j.DriverWithContext, db, label string) int64 {
	t.Helper()
	res, err := neo4j.ExecuteQuery(ctx, drv,
		"MATCH (n:"+label+") RETURN count(n) AS n",
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(db))
	if err != nil {
		t.Fatalf("countLabel(%s): %v", label, err)
	}
	v, _ := res.Records[0].Get("n")
	n, _ := v.(int64)
	return n
}

// countRel returns the total number of relationships with the given type.
func countRel(t *testing.T, ctx context.Context, drv neo4j.DriverWithContext, db, relType string) int64 {
	t.Helper()
	res, err := neo4j.ExecuteQuery(ctx, drv,
		"MATCH ()-[r:"+relType+"]->() RETURN count(r) AS n",
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(db))
	if err != nil {
		t.Fatalf("countRel(%s): %v", relType, err)
	}
	v, _ := res.Records[0].Get("n")
	n, _ := v.(int64)
	return n
}

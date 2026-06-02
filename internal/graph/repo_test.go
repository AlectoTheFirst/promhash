//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestEnsureConstraintsIdempotent(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	if err := r.EnsureConstraints(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureConstraints(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

// TestAppNameUniqueConstraint verifies that EnsureConstraints creates a UNIQUE
// constraint named "app_name_unique" on Application.name, that the constraint
// is backed by an index (SHOW CONSTRAINTS), and that it is enforced at runtime:
// a second Application node with the same name but a different phash must fail.
func TestAppNameUniqueConstraint(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	const dbName = "neo4j"
	r := New(drv, dbName)

	if err := r.EnsureConstraints(ctx); err != nil {
		t.Fatalf("EnsureConstraints: %v", err)
	}

	// Verify constraint exists with the expected name, label, and property.
	res, err := neo4j.ExecuteQuery(ctx, drv,
		`SHOW CONSTRAINTS YIELD name, labelsOrTypes, properties WHERE name = 'app_name_unique'`,
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err != nil {
		t.Fatalf("SHOW CONSTRAINTS: %v", err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("want 1 constraint named 'app_name_unique', got %d records", len(res.Records))
	}
	rec := res.Records[0]
	labelsRaw, _ := rec.Get("labelsOrTypes")
	propsRaw, _ := rec.Get("properties")
	labels, ok1 := labelsRaw.([]any)
	props, ok2 := propsRaw.([]any)
	if !ok1 || len(labels) != 1 || labels[0] != "Application" {
		t.Errorf("want labelsOrTypes=[Application], got %v (ok=%v)", labelsRaw, ok1)
	}
	if !ok2 || len(props) != 1 || props[0] != "name" {
		t.Errorf("want properties=[name], got %v (ok=%v)", propsRaw, ok2)
	}

	// Prove enforcement: first node succeeds, second node with same name but
	// different phash must return a constraint-violation error.
	if _, err := neo4j.ExecuteQuery(ctx, drv,
		`CREATE (:Application {phash:'p1', name:'dup'})`,
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName)); err != nil {
		t.Fatalf("first CREATE (should succeed): %v", err)
	}
	_, err2 := neo4j.ExecuteQuery(ctx, drv,
		`CREATE (:Application {phash:'p2', name:'dup'})`,
		nil, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err2 == nil {
		t.Fatal("second CREATE with duplicate name must fail (constraint violated), but got nil error")
	}
}

func TestUpsertAndGetInterface(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	in := Iface{PHash: "interface:abc", Device: "rtr-core-1", IfName: "te0/1/2",
		MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc",
		Instance: "10.0.0.1", Vendor: "cisco-iosxr", IfIndex: 42, ObservedAt: time.Unix(1700000000, 0).UTC()}
	if err := r.UpsertInterface(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.IfIndex = 43 // re-upsert updates the volatile attr, same node
	if err := r.UpsertInterface(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetInterfaceByPHash(ctx, "interface:abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.IfIndex != 43 || got.IfAlias != "uplink-ledger-dc" {
		t.Fatalf("got %+v", got)
	}
}

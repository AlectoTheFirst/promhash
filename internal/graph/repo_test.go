//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

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

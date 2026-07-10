//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestUpsertInterfacesBatch(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	obs := time.Unix(1700000000, 0).UTC()
	batch := []Iface{
		{PHash: "interface:batch-1", Device: "rtr-b1", IfName: "te0/0/1", MetricIfName: "Te0/0/1",
			Instance: "10.1.0.1", IfIndex: 1, Vendor: "cisco", ObservedAt: obs},
		{PHash: "interface:batch-2", Device: "rtr-b1", IfName: "te0/0/2", MetricIfName: "Te0/0/2",
			Instance: "10.1.0.1", IfIndex: 2, Vendor: "cisco", ObservedAt: obs},
		{PHash: "interface:batch-3", Device: "rtr-b2", IfName: "ge-0/0/3", MetricIfName: "ge-0/0/3",
			Instance: "10.1.0.2", IfIndex: 3, Vendor: "juniper", ObservedAt: obs},
	}
	if err := r.UpsertInterfaces(ctx, batch); err != nil {
		t.Fatalf("UpsertInterfaces: %v", err)
	}

	// Idempotent re-run with an updated property must not duplicate nodes.
	batch[1].IfAlias = "uplink-renamed"
	if err := r.UpsertInterfaces(ctx, batch); err != nil {
		t.Fatalf("UpsertInterfaces (rerun): %v", err)
	}

	all, err := r.ListAllInterfaces(ctx)
	if err != nil {
		t.Fatalf("ListAllInterfaces: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 interfaces after idempotent rerun, got %d", len(all))
	}
	got, err := r.GetInterfaceByPHash(ctx, "interface:batch-2")
	if err != nil {
		t.Fatalf("GetInterfaceByPHash: %v", err)
	}
	if got.IfAlias != "uplink-renamed" {
		t.Fatalf("rerun did not overwrite properties: IfAlias=%q", got.IfAlias)
	}
	if !got.ObservedAt.Equal(obs) {
		t.Fatalf("ObservedAt round-trip: got %v want %v", got.ObservedAt, obs)
	}

	// Empty batch is a no-op, not an error.
	if err := r.UpsertInterfaces(ctx, nil); err != nil {
		t.Fatalf("UpsertInterfaces(nil): %v", err)
	}
}

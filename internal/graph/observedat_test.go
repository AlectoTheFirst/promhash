//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestUpsertInterfaceObservedAtRoundTrip(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	const phash = "interface:observedat-roundtrip"
	ts1 := time.Unix(1700000000, 0).UTC()

	// Write with ts1 and confirm it reads back.
	in := Iface{
		PHash: phash, Device: "rtr-obs-1", IfName: "gi0/0/0",
		MetricIfName: "Gi0/0/0", IfDescr: "GigabitEthernet0/0/0",
		Instance: "10.0.0.99", Vendor: "cisco", IfIndex: 7,
		ObservedAt: ts1,
	}
	if err := r.UpsertInterface(ctx, in); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	got, err := r.GetInterfaceByPHash(ctx, phash)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	if got.ObservedAt.Unix() != ts1.Unix() {
		t.Fatalf("after first upsert: want ObservedAt %d, got %d", ts1.Unix(), got.ObservedAt.Unix())
	}

	// Re-upsert at a later timestamp and confirm the read-back reflects it.
	ts2 := time.Unix(1700086400, 0).UTC() // ts1 + 24 h
	in.ObservedAt = ts2
	if err := r.UpsertInterface(ctx, in); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got2, err := r.GetInterfaceByPHash(ctx, phash)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if got2.ObservedAt.Unix() != ts2.Unix() {
		t.Fatalf("after second upsert: want ObservedAt %d, got %d", ts2.Unix(), got2.ObservedAt.Unix())
	}

	// Verify via ListAllInterfaces as well.
	all, err := r.ListAllInterfaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, ifc := range all {
		if ifc.PHash == phash {
			if ifc.ObservedAt.Unix() != ts2.Unix() {
				t.Fatalf("ListAllInterfaces: want ObservedAt %d, got %d", ts2.Unix(), ifc.ObservedAt.Unix())
			}
			found = true
		}
	}
	if !found {
		t.Fatal("interface not found via ListAllInterfaces")
	}
}

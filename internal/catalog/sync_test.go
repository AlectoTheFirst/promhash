//go:build integration

package catalog

import (
	"context"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestSyncUpsertsInterfaces(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	rows := []promclient.IfaceRow{{Instance: "10.0.0.1", IfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", IfIndex: 42}}
	devByInstance := map[string]string{"10.0.0.1": "rtr-core-1"}
	if err := Sync(ctx, r, rows, devByInstance, "cisco"); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetInterfaceByPHash(ctx, ifacePHash("rtr-core-1", "tengige0/1/2"))
	if err != nil {
		t.Fatal(err)
	}
	if got.MetricIfName != "Te0/1/2" || got.Device != "rtr-core-1" {
		t.Fatalf("got %+v", got)
	}
}

// TestSyncEmptyCanonSkipped verifies that rows whose IfName and IfDescr both
// canonicalize to "" are skipped rather than upserted, so no degenerate shared
// phash node is minted for unnamed interfaces on a device.
func TestSyncEmptyCanonSkipped(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	// Two rows on the same device, both with empty IfName and IfDescr.
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.9", IfName: "", IfDescr: "", IfAlias: "alias-a", IfIndex: 1},
		{Instance: "10.0.0.9", IfName: "", IfDescr: "", IfAlias: "alias-b", IfIndex: 2},
	}
	devByInstance := map[string]string{"10.0.0.9": "rtr-empty-test"}
	if err := Sync(ctx, r, rows, devByInstance, "cisco"); err != nil {
		t.Fatal(err)
	}

	// The degenerate phash (device + "") must not exist.
	degeneratePHash := ifacePHash("rtr-empty-test", "")
	_, err := r.GetInterfaceByPHash(ctx, degeneratePHash)
	if err == nil {
		t.Fatal("expected ErrNotFound for degenerate phash, but node was found")
	}
	if err != graph.ErrNotFound {
		t.Fatalf("expected graph.ErrNotFound, got %v", err)
	}
}

// TestSyncCaseVariantsCollapseToOneNode verifies that two Sync calls for the
// same interface whose device names differ only in case — "Rtr1" and "rtr1" —
// produce exactly ONE Interface node (same phash), and that a Resolver built
// from ListAllInterfaces resolves either casing to that node.
func TestSyncCaseVariantsCollapseToOneNode(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	row := promclient.IfaceRow{
		Instance: "10.0.0.2", IfName: "Te0/1/0", IfDescr: "TenGigE0/1/0",
		IfAlias: "uplink-core", IfIndex: 1,
	}

	// First sync with uppercase device name.
	if err := Sync(ctx, r, []promclient.IfaceRow{row},
		map[string]string{"10.0.0.2": "Rtr1"}, "cisco"); err != nil {
		t.Fatal(err)
	}
	// Second sync with lowercase device name — must collapse to same node.
	if err := Sync(ctx, r, []promclient.IfaceRow{row},
		map[string]string{"10.0.0.2": "rtr1"}, "cisco"); err != nil {
		t.Fatal(err)
	}

	all, err := r.ListAllInterfaces(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Filter to just the nodes for this device/interface.
	var matches []graph.Iface
	wantPHash := ifacePHash("rtr1", "tengige0/1/0")
	for _, ifc := range all {
		if ifc.PHash == wantPHash {
			matches = append(matches, ifc)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 node with phash %q; got %d node(s): %+v",
			wantPHash, len(matches), matches)
	}
	// Stored device must be the normalized form.
	if matches[0].Device != "rtr1" {
		t.Errorf("stored Device = %q; want %q", matches[0].Device, "rtr1")
	}

	// Resolver must return the node for either casing.
	res := NewResolver(all)
	for _, dev := range []string{"rtr1", "Rtr1", "  RTR1  "} {
		got, err := res.Resolve(dev, "Te0/1/0")
		if err != nil {
			t.Errorf("Resolve(%q): %v", dev, err)
			continue
		}
		if got.PHash != wantPHash {
			t.Errorf("Resolve(%q): PHash = %q; want %q", dev, got.PHash, wantPHash)
		}
	}
}

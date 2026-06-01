//go:build integration

package declare

import (
	"context"
	"testing"
	"time"

	"github.com/starkweb/promhash/internal/catalog"
	"github.com/starkweb/promhash/internal/graph"
	"github.com/starkweb/promhash/internal/testutil"
)

func TestLoadCreatesPathHops(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	// seed catalog interfaces the declaration references
	seedCatalog(t, ctx, r)
	a, _ := Parse([]byte(sample))
	res := catalog.NewResolver(loadCatalog(t, ctx, r))
	if errs := Validate(a, res); len(errs) != 0 {
		t.Fatalf("validate: %v", errs)
	}
	if err := Load(ctx, r, a, res, "deadbeef", time.Unix(1700000000, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	hops, err := r.AppPath(ctx, appPHash("payments"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) == 0 {
		t.Fatal("expected hops on payments path")
	}
}

// seedCatalog upserts the two interfaces from resolver() so the declaration's
// (device, if) references resolve and the HOP MATCH finds real Interface nodes.
func seedCatalog(t *testing.T, ctx context.Context, r *graph.Repo) {
	t.Helper()
	for _, ifc := range resolverIfaces() {
		if err := r.UpsertInterface(ctx, ifc); err != nil {
			t.Fatalf("seed catalog: %v", err)
		}
	}
}

// loadCatalog lists the seeded interfaces back from the graph.
func loadCatalog(t *testing.T, ctx context.Context, r *graph.Repo) []graph.Iface {
	t.Helper()
	var out []graph.Iface
	for _, want := range resolverIfaces() {
		got, err := r.GetInterfaceByPHash(ctx, want.PHash)
		if err != nil {
			t.Fatalf("load catalog %s: %v", want.PHash, err)
		}
		out = append(out, got)
	}
	return out
}

// resolverIfaces mirrors the unit-test resolver()'s interface set.
func resolverIfaces() []graph.Iface {
	return []graph.Iface{
		{PHash: "interface:1", Device: "rtr-acc-fra-1", IfName: "tengige0/1/2", MetricIfName: "Te0/1/2", Vendor: "cisco"},
		{PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/2/1", MetricIfName: "Te0/2/1", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
	}
}

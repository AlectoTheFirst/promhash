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

func TestCloseValiditySetsValidTo(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedCatalog(t, ctx, r)
	a, _ := Parse([]byte(sample))
	res := catalog.NewResolver(loadCatalog(t, ctx, r))
	_ = Load(ctx, r, a, res, "sha1", time.Unix(1700000000, 0).UTC())
	closeAt := time.Unix(1700000900, 0).UTC()
	if err := r.CloseAppValidity(ctx, appPHash("payments"), closeAt); err != nil {
		t.Fatal(err)
	}
	hops, _ := r.AppPath(ctx, appPHash("payments"), time.Now())
	if len(hops) != 0 {
		t.Fatalf("expected no current hops after retraction, got %d", len(hops))
	}
	past, _ := r.AppPath(ctx, appPHash("payments"), time.Unix(1700000100, 0).UTC())
	if len(past) == 0 {
		t.Fatal("expected historical hops to remain queryable")
	}
}

// supersedeRev2 re-declares the same app (payments) with a DIFFERENT, smaller
// path that resolves against the same seeded catalog: a single hop on rtr-core-1
// (uplink-ledger-dc -> interface:2). The first revision (sample) traverses both
// rtr-acc-fra-1 and rtr-core-1, so the revisions are distinguishable by their
// hop sets.
const supersedeRev2 = `
app: payments
runs_as: payments-api
owner: team-payments
consumed_by_customers: [acme, globex]
depends_on:
  - to: ledger-api
    paths:
      - hops:
          - {device: rtr-core-1, if: "uplink-ledger-dc", direction: egress}
`

// TestLoadSupersedeReplacesPath asserts Load is idempotent across revisions:
// loading the same app twice with different paths/validFrom must supersede the
// first revision rather than union with it. AppPath(now) must return ONLY the
// second revision's hops, while a point-in-time query inside the first
// revision's validity window must still return the first revision's hops.
func TestLoadSupersedeReplacesPath(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedCatalog(t, ctx, r)
	res := catalog.NewResolver(loadCatalog(t, ctx, r))

	t1 := time.Unix(1700000000, 0).UTC()
	t2 := time.Unix(1700001000, 0).UTC() // later: second revision supersedes at t2

	// Revision 1: the two-hop sample path.
	rev1, _ := Parse([]byte(sample))
	if errs := Validate(rev1, res); len(errs) != 0 {
		t.Fatalf("validate rev1: %v", errs)
	}
	if err := Load(ctx, r, rev1, res, "rev1", t1); err != nil {
		t.Fatalf("load rev1: %v", err)
	}

	// Revision 2: a single-hop path on rtr-core-1, loaded at a later validFrom.
	rev2, _ := Parse([]byte(supersedeRev2))
	if errs := Validate(rev2, res); len(errs) != 0 {
		t.Fatalf("validate rev2: %v", errs)
	}
	if err := Load(ctx, r, rev2, res, "rev2", t2); err != nil {
		t.Fatalf("load rev2: %v", err)
	}

	// At "now" only revision 2 is open: exactly its single rtr-core-1 hop, with
	// no stale rtr-acc-fra-1 hop unioned in.
	now, err := r.AppPath(ctx, appPHash("payments"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if devs := hopDevices(now); len(devs) != 1 || !devs["rtr-core-1"] {
		t.Fatalf("AppPath(now): want ONLY rev2 hop on rtr-core-1, got %d hops %v", len(now), devs)
	}
	if devs := hopDevices(now); devs["rtr-acc-fra-1"] {
		t.Fatalf("AppPath(now): stale rev1 hop on rtr-acc-fra-1 leaked into current path: %v", devs)
	}

	// A point-in-time query inside the first window (t1 <= at < t2) still returns
	// revision 1's two-hop path.
	past, err := r.AppPath(ctx, appPHash("payments"), t1.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	devs := hopDevices(past)
	if len(devs) != 2 || !devs["rtr-acc-fra-1"] || !devs["rtr-core-1"] {
		t.Fatalf("AppPath(in rev1 window): want rev1 two-hop path {rtr-acc-fra-1, rtr-core-1}, got %d hops %v",
			len(past), devs)
	}
}

// hopDevices returns the set of distinct hop device names.
func hopDevices(hops []graph.Hop) map[string]bool {
	devs := map[string]bool{}
	for _, h := range hops {
		devs[h.Device] = true
	}
	return devs
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

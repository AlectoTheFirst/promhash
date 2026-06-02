//go:build integration

package graph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// seedIface upserts a single Interface node and fails the test on error.
func seedIface(t *testing.T, ctx context.Context, r *Repo, phash, device, ifName string) {
	t.Helper()
	if err := r.UpsertInterface(ctx, Iface{
		PHash: phash, Device: device, IfName: ifName,
		MetricIfName: ifName, Instance: "10.0.0.1", IfIndex: 1,
		ObservedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatalf("seedIface %s: %v", phash, err)
	}
}

func buildReloadApp(validFrom time.Time, hopPHash, hopDevice string) DeclaredApp {
	return DeclaredApp{
		AppPHash:    "application:reload-test",
		App:         "reload-test",
		AppSvcPHash: "appservice:reload-test",
		AppSvc:      "reload-test",
		Owner:       "team-reload",
		Customers:   nil,
		Source:      "reload-test",
		ValidFrom:   validFrom,
		Deps: []DeclaredDep{{
			ToAppSvc: "appservice:reload-target",
			ToName:   "reload-target",
			Paths: []DeclaredPath{{
				Hops: []DeclaredHop{{IfacePHash: hopPHash, Seq: 1, Direction: "egress"}},
			}},
		}},
	}
}

// TestReloadSupersedesInOneTx verifies that two sequential ReloadDeclaredApp calls
// produce a clean supersede: AppPath(now) returns ONLY the second revision's hop.
func TestReloadSupersedesInOneTx(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:reload-a", "rtr-reload-a", "te0/0/1")
	seedIface(t, ctx, r, "interface:reload-b", "rtr-reload-b", "te0/0/2")

	t1 := time.Unix(1700000000, 0).UTC()
	t2 := time.Unix(1700001000, 0).UTC()

	// Revision 1: hop on rtr-reload-a.
	rev1 := buildReloadApp(t1, "interface:reload-a", "rtr-reload-a")
	if err := r.ReloadDeclaredApp(ctx, rev1, t1); err != nil {
		t.Fatalf("ReloadDeclaredApp rev1: %v", err)
	}

	hops1, err := r.AppPath(ctx, "application:reload-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops1) != 1 || hops1[0].Device != "rtr-reload-a" {
		t.Fatalf("after rev1: want 1 hop on rtr-reload-a, got %d hops %+v", len(hops1), hops1)
	}

	// Revision 2: hop on rtr-reload-b; supersedes rev1.
	rev2 := buildReloadApp(t2, "interface:reload-b", "rtr-reload-b")
	if err := r.ReloadDeclaredApp(ctx, rev2, t2); err != nil {
		t.Fatalf("ReloadDeclaredApp rev2: %v", err)
	}

	hops2, err := r.AppPath(ctx, "application:reload-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops2) != 1 {
		t.Fatalf("after rev2: want exactly 1 hop, got %d hops %+v", len(hops2), hops2)
	}
	if hops2[0].Device != "rtr-reload-b" {
		t.Fatalf("after rev2: want hop on rtr-reload-b, got device=%q", hops2[0].Device)
	}
	for _, h := range hops2 {
		if h.Device == "rtr-reload-a" {
			t.Fatalf("after rev2: stale rev1 hop on rtr-reload-a leaked into current path: %+v", hops2)
		}
	}
}

// TestSameSecondReloadKeepsQueryableWindow verifies the D2 monotonic-bump
// invariant: two ReloadDeclaredApp calls at the identical wall-clock second
// must NOT produce a zero-width validity window for the first revision.
//
// Assertions:
//
//	(a) AppPath(now) returns exactly rev2's hop (current state is rev2).
//	(b) rev1's validTo is strictly greater than rev1's validFrom (positive-width
//	    window); equivalently rev2.validFrom == rev1.validFrom + 1s.
//	(c) rev2.validFrom is strictly greater than rev1.validFrom.
func TestSameSecondReloadKeepsQueryableWindow(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	const dbName = "neo4j"
	r := New(drv, dbName)
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:same-sec-a", "rtr-same-a", "te1/0/1")
	seedIface(t, ctx, r, "interface:same-sec-b", "rtr-same-b", "te1/0/2")

	// Both reloads use the SAME wall-clock second (T).
	T := time.Unix(1700005000, 0).UTC()

	// Rev1: hop on rtr-same-a.
	rev1 := buildReloadApp(T, "interface:same-sec-a", "rtr-same-a")
	rev1.AppPHash = "application:same-sec-test"
	rev1.App = "same-sec-test"
	rev1.AppSvcPHash = "appservice:same-sec-test"
	rev1.AppSvc = "same-sec-test"
	if err := r.ReloadDeclaredApp(ctx, rev1, T); err != nil {
		t.Fatalf("ReloadDeclaredApp rev1: %v", err)
	}

	// Rev2: different hop, same timestamp T.
	rev2 := buildReloadApp(T, "interface:same-sec-b", "rtr-same-b")
	rev2.AppPHash = "application:same-sec-test"
	rev2.App = "same-sec-test"
	rev2.AppSvcPHash = "appservice:same-sec-test"
	rev2.AppSvc = "same-sec-test"
	if err := r.ReloadDeclaredApp(ctx, rev2, T); err != nil {
		t.Fatalf("ReloadDeclaredApp rev2: %v", err)
	}

	// (a) Current path must be rev2's hop only.
	hops, err := r.AppPath(ctx, "application:same-sec-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-same-b" {
		t.Fatalf("(a) want 1 hop on rtr-same-b (rev2), got %d hops %+v", len(hops), hops)
	}

	// Read all DEPENDS_ON edges ordered by validFrom so we can inspect both revisions.
	res, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc:ApplicationService)
		 MATCH (svc)-[do:DEPENDS_ON]->(target)
		 RETURN do.validFrom AS vf, do.validTo AS vt
		 ORDER BY vf`,
		map[string]any{"p": "application:same-sec-test"},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err != nil {
		t.Fatalf("query DEPENDS_ON edges: %v", err)
	}
	if got := len(res.Records); got != 2 {
		t.Fatalf("want 2 DEPENDS_ON edges (rev1 closed, rev2 open), got %d", got)
	}

	vf0raw, _ := res.Records[0].Get("vf")
	vt0raw, _ := res.Records[0].Get("vt")
	vf1raw, _ := res.Records[1].Get("vf")

	vf0, ok0 := vf0raw.(int64)
	if !ok0 {
		t.Fatalf("rev1 validFrom not int64: %T %v", vf0raw, vf0raw)
	}
	vt0, ok1 := vt0raw.(int64)
	if !ok1 {
		t.Fatalf("rev1 validTo not int64 (nil means zero-width was not bumped): %T %v", vt0raw, vt0raw)
	}
	vf1, ok2 := vf1raw.(int64)
	if !ok2 {
		t.Fatalf("rev2 validFrom not int64: %T %v", vf1raw, vf1raw)
	}

	// (b) rev1's window must be positive-width (validTo > validFrom).
	if vt0 <= vf0 {
		t.Errorf("(b) rev1 zero-width window: validFrom=%d validTo=%d (want validTo > validFrom)", vf0, vt0)
	}

	// (c) rev2.validFrom must be strictly greater than rev1.validFrom.
	if vf1 <= vf0 {
		t.Errorf("(c) rev2.validFrom=%d not strictly greater than rev1.validFrom=%d", vf1, vf0)
	}

	// The bump must be exactly +1s: rev2.validFrom == rev1.validFrom + 1.
	if vf1 != vf0+1 {
		t.Errorf("bump: want rev2.validFrom == rev1.validFrom+1 (%d), got %d", vf0+1, vf1)
	}
	// And the close must be at the bumped time: rev1.validTo == rev2.validFrom.
	if vt0 != vf1 {
		t.Errorf("contiguous: want rev1.validTo (%d) == rev2.validFrom (%d)", vt0, vf1)
	}
}

// TestSameSecondControlNoBump is the control: two reloads 1000s apart must NOT
// trigger the bump. rev2.validFrom must equal its requested timestamp exactly.
func TestSameSecondControlNoBump(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	const dbName = "neo4j"
	r := New(drv, dbName)
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:ctrl-a", "rtr-ctrl-a", "te2/0/1")
	seedIface(t, ctx, r, "interface:ctrl-b", "rtr-ctrl-b", "te2/0/2")

	t1 := time.Unix(1700010000, 0).UTC()
	t2 := time.Unix(1700011000, 0).UTC() // 1000s later — no bump needed

	rev1 := buildReloadApp(t1, "interface:ctrl-a", "rtr-ctrl-a")
	rev1.AppPHash = "application:ctrl-test"
	rev1.App = "ctrl-test"
	rev1.AppSvcPHash = "appservice:ctrl-test"
	rev1.AppSvc = "ctrl-test"
	if err := r.ReloadDeclaredApp(ctx, rev1, t1); err != nil {
		t.Fatalf("ReloadDeclaredApp rev1: %v", err)
	}

	rev2 := buildReloadApp(t2, "interface:ctrl-b", "rtr-ctrl-b")
	rev2.AppPHash = "application:ctrl-test"
	rev2.App = "ctrl-test"
	rev2.AppSvcPHash = "appservice:ctrl-test"
	rev2.AppSvc = "ctrl-test"
	if err := r.ReloadDeclaredApp(ctx, rev2, t2); err != nil {
		t.Fatalf("ReloadDeclaredApp rev2: %v", err)
	}

	res, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc:ApplicationService)
		 MATCH (svc)-[do:DEPENDS_ON]->(target)
		 RETURN do.validFrom AS vf, do.validTo AS vt
		 ORDER BY vf`,
		map[string]any{"p": "application:ctrl-test"},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(dbName))
	if err != nil {
		t.Fatalf("query DEPENDS_ON edges: %v", err)
	}
	if got := len(res.Records); got != 2 {
		t.Fatalf("want 2 DEPENDS_ON edges, got %d", got)
	}

	vf1raw, _ := res.Records[1].Get("vf")
	vf1, ok := vf1raw.(int64)
	if !ok {
		t.Fatalf("rev2 validFrom not int64: %T %v", vf1raw, vf1raw)
	}
	// No bump: rev2.validFrom must equal the requested t2 exactly.
	if vf1 != t2.Unix() {
		t.Errorf("control: want rev2.validFrom=%d (no bump), got %d", t2.Unix(), vf1)
	}
}

// TestReloadRollsBackOnError is the atomicity proof: it exercises the scenario
// where the close runs but the transaction returns an error (simulating a crash
// mid-reload). ExecuteWrite must roll back the entire transaction, so the
// original hops remain visible. This test FAILS against the old design of four
// separate auto-committed writes, proving atomicity is a property of the new
// managed-transaction approach.
func TestReloadRollsBackOnError(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:atomic-a", "rtr-atomic-a", "te0/0/1")

	t1 := time.Unix(1700000000, 0).UTC()
	t2 := time.Unix(1700001000, 0).UTC()

	// Establish a valid revision 1 via ReloadDeclaredApp.
	rev1 := buildReloadApp(t1, "interface:atomic-a", "rtr-atomic-a")
	if err := r.ReloadDeclaredApp(ctx, rev1, t1); err != nil {
		t.Fatalf("setup ReloadDeclaredApp rev1: %v", err)
	}

	// Confirm rev1 is visible.
	hops, err := r.AppPath(ctx, "application:reload-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-atomic-a" {
		t.Fatalf("setup: want 1 hop on rtr-atomic-a, got %d hops %+v", len(hops), hops)
	}

	// Now simulate a partial reload: close runs, then the tx function returns an
	// error before the upsert. The managed transaction must roll back the close.
	rollbackErr := r.execWrite(ctx, func(tx neo4j.ManagedTransaction) error {
		if err := closeAppValidityTx(ctx, tx, "application:reload-test", t2); err != nil {
			return err
		}
		return errors.New("boom: injected failure to prove rollback")
	})
	if rollbackErr == nil {
		t.Fatal("expected execWrite to return an error, got nil")
	}

	// The close was rolled back — rev1 hops must still be visible at "now".
	hopsAfter, err := r.AppPath(ctx, "application:reload-test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hopsAfter) != 1 {
		t.Fatalf("rollback proof: want 1 hop (close rolled back), got %d hops %+v", len(hopsAfter), hopsAfter)
	}
	if hopsAfter[0].Device != "rtr-atomic-a" {
		t.Fatalf("rollback proof: want device=rtr-atomic-a, got %q", hopsAfter[0].Device)
	}
}

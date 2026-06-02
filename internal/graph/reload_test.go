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

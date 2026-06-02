//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// reconcileFunc mirrors the reconcile function in cmd/promhash-loader/main.go
// so that the reconcile logic can be integration-tested against a real Neo4j
// container without importing package main. Any change to the main.go reconcile
// must be reflected here.
func reconcileFunc(ctx context.Context, r *Repo, present map[string]bool, now time.Time) (int, error) {
	open, err := r.ListOpenDeclaredApps(ctx)
	if err != nil {
		return 0, err
	}
	var n int
	for _, p := range open {
		if present[p] {
			continue
		}
		if err := r.CloseAppValidity(ctx, p, now); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// paymentsPHash and ledgerPHash are the canonical phashes used across the
// reconcile tests, matching what phash.Hash(phash.KindApp, ...) produces.
var (
	paymentsPHash = phash.Hash(phash.KindApp, "payments")
	ledgerPHash   = phash.Hash(phash.KindApp, "ledger")
)

// seedDeclaredApp loads a minimal DeclaredApp with one hop for the given
// appName via ReloadDeclaredApp. The interface node must already exist.
func seedDeclaredApp(t *testing.T, ctx context.Context, r *Repo, appName, ifacePHash string, validFrom time.Time) {
	t.Helper()
	appP := phash.Hash(phash.KindApp, appName)
	svcP := phash.Hash(phash.KindAppSvc, appName)
	d := DeclaredApp{
		AppPHash:    appP,
		App:         appName,
		AppSvcPHash: svcP,
		AppSvc:      appName,
		Owner:       "team-" + appName,
		Customers:   nil,
		Source:      "reconcile-test",
		ValidFrom:   validFrom,
		Deps: []DeclaredDep{{
			ToAppSvc: phash.Hash(phash.KindAppSvc, "external"),
			ToName:   "external",
			Paths: []DeclaredPath{{
				Hops: []DeclaredHop{{IfacePHash: ifacePHash, Seq: 1, Direction: "egress"}},
			}},
		}},
	}
	if err := r.ReloadDeclaredApp(ctx, d, validFrom); err != nil {
		t.Fatalf("seedDeclaredApp %s: %v", appName, err)
	}
}

// TestListOpenDeclaredApps verifies that ListOpenDeclaredApps returns exactly
// the phashes of apps with open declared DEPENDS_ON edges, and excludes apps
// whose edges have been closed with CloseAppValidity.
func TestListOpenDeclaredApps(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	// Seed two shared interface nodes.
	seedIface(t, ctx, r, "interface:list-hop-p", "rtr-list-p", "te0/0/1")
	seedIface(t, ctx, r, "interface:list-hop-l", "rtr-list-l", "te0/0/2")

	t1 := time.Unix(1701000000, 0).UTC()

	// Load payments and ledger.
	seedDeclaredApp(t, ctx, r, "payments", "interface:list-hop-p", t1)
	seedDeclaredApp(t, ctx, r, "ledger", "interface:list-hop-l", t1)

	// Both should appear as open.
	open, err := r.ListOpenDeclaredApps(ctx)
	if err != nil {
		t.Fatalf("ListOpenDeclaredApps: %v", err)
	}
	openSet := make(map[string]bool)
	for _, p := range open {
		openSet[p] = true
	}
	if !openSet[paymentsPHash] {
		t.Errorf("want payments (%s) in open set, got %v", paymentsPHash, open)
	}
	if !openSet[ledgerPHash] {
		t.Errorf("want ledger (%s) in open set, got %v", ledgerPHash, open)
	}

	// Close ledger's validity; it should disappear from the open set.
	t2 := time.Unix(1701001000, 0).UTC()
	if err := r.CloseAppValidity(ctx, ledgerPHash, t2); err != nil {
		t.Fatalf("CloseAppValidity ledger: %v", err)
	}

	open2, err := r.ListOpenDeclaredApps(ctx)
	if err != nil {
		t.Fatalf("ListOpenDeclaredApps after close: %v", err)
	}
	open2Set := make(map[string]bool)
	for _, p := range open2 {
		open2Set[p] = true
	}
	if !open2Set[paymentsPHash] {
		t.Errorf("want payments still open after closing ledger, got %v", open2)
	}
	if open2Set[ledgerPHash] {
		t.Errorf("want ledger removed from open set after CloseAppValidity, got %v", open2)
	}
}

// TestReconcileRetractsRemoved verifies that the reconcile function:
//  1. Closes validity for an app absent from the present set (tombstone).
//  2. AppPath(now) returns 0 hops after retraction.
//  3. AppPath(t1+30s) — a point-in-time BEFORE the retraction — still returns
//     the historical hops (history is preserved, not deleted).
//  4. Returns the correct retraction count.
func TestReconcileRetractsRemoved(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:retract-p", "rtr-retract-p", "te1/0/1")

	t1 := time.Unix(1702000000, 0).UTC()
	seedDeclaredApp(t, ctx, r, "payments", "interface:retract-p", t1)

	// Confirm payments is visible at now before retraction.
	hopsBefore, err := r.AppPath(ctx, paymentsPHash, time.Now())
	if err != nil {
		t.Fatalf("AppPath before retract: %v", err)
	}
	if len(hopsBefore) == 0 {
		t.Fatal("setup: want >0 hops before retraction")
	}

	// Run reconcile with an empty present set (payments was deleted).
	t2 := time.Unix(1702001000, 0).UTC()
	present := map[string]bool{} // payments NOT present
	n, err := reconcileFunc(ctx, r, present, t2)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Errorf("want retraction count=1, got %d", n)
	}

	// After retraction: AppPath(now) must return 0 hops.
	hopsAfter, err := r.AppPath(ctx, paymentsPHash, time.Now())
	if err != nil {
		t.Fatalf("AppPath after retract: %v", err)
	}
	if len(hopsAfter) != 0 {
		t.Errorf("want 0 hops after retraction, got %d: %+v", len(hopsAfter), hopsAfter)
	}

	// History preserved: AppPath at t1+30s (before t2) must still return hops.
	pointInTime := t1.Add(30 * time.Second)
	hopsHist, err := r.AppPath(ctx, paymentsPHash, pointInTime)
	if err != nil {
		t.Fatalf("AppPath historical: %v", err)
	}
	if len(hopsHist) == 0 {
		t.Errorf("want historical hops at t1+30s (before retraction), got 0 — history was deleted not closed")
	}
}

// TestReconcileKeepsPresent verifies that an app present in the present set
// is NOT retracted (no spurious tombstone) and the count is 0.
func TestReconcileKeepsPresent(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:keep-p", "rtr-keep-p", "te2/0/1")

	t1 := time.Unix(1703000000, 0).UTC()
	seedDeclaredApp(t, ctx, r, "payments", "interface:keep-p", t1)

	hops1, err := r.AppPath(ctx, paymentsPHash, time.Now())
	if err != nil {
		t.Fatalf("AppPath before reconcile: %v", err)
	}
	if len(hops1) == 0 {
		t.Fatal("setup: want >0 hops before reconcile")
	}

	// Run reconcile with payments IN the present set.
	t2 := time.Unix(1703001000, 0).UTC()
	present := map[string]bool{paymentsPHash: true}
	n, err := reconcileFunc(ctx, r, present, t2)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Errorf("want retraction count=0 when app is present, got %d", n)
	}

	// AppPath(now) must still return the same hops.
	hops2, err := r.AppPath(ctx, paymentsPHash, time.Now())
	if err != nil {
		t.Fatalf("AppPath after reconcile: %v", err)
	}
	if len(hops2) != len(hops1) {
		t.Errorf("want %d hops unchanged after reconcile, got %d: %+v", len(hops1), len(hops2), hops2)
	}
}

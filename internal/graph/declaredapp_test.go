//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// TestUpsertDeclaredAppNoCustomers proves the FOREACH fix: a declared app with
// an empty Customers list must still create its dependency topology (DEPENDS_ON,
// Connection, Path, HOP), so AppPath returns the declared hops. The previous
// `UNWIND $customers AS cust` chained all downstream creation, so an empty
// customers list collapsed cardinality to zero rows and silently dropped every
// dependency.
func TestUpsertDeclaredAppNoCustomers(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	// The hop MATCHes an existing Interface, so seed it first.
	if err := r.UpsertInterface(ctx, Iface{
		PHash: "interface:hop1", Device: "rtr-a", IfName: "te0/0/1",
		MetricIfName: "Te0/0/1", Instance: "10.0.0.1", IfIndex: 7,
		ObservedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatalf("seed interface: %v", err)
	}

	validFrom := time.Unix(1700000000, 0).UTC()
	d := DeclaredApp{
		AppPHash:    "application:payments",
		App:         "payments",
		AppSvcPHash: "appservice:payments",
		AppSvc:      "payments",
		Owner:       "team-payments",
		Customers:   nil, // no consumers — the case that used to drop all deps
		Source:      "declare-test",
		ValidFrom:   validFrom,
		Deps: []DeclaredDep{{
			ToAppSvc: "appservice:ledger",
			ToName:   "ledger",
			Paths: []DeclaredPath{{
				Hops: []DeclaredHop{{IfacePHash: "interface:hop1", Seq: 1, Direction: "egress"}},
			}},
		}},
	}
	if err := r.UpsertDeclaredApp(ctx, d); err != nil {
		t.Fatalf("upsert declared app: %v", err)
	}

	hops, err := r.AppPath(ctx, "application:payments", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != 1 {
		t.Fatalf("want exactly 1 declared hop despite empty customers, got %d (%+v)", len(hops), hops)
	}
	h := hops[0]
	if h.Device != "rtr-a" || h.IfName != "te0/0/1" || h.Seq != 1 || h.Direction != "egress" {
		t.Fatalf("unexpected hop: %+v", h)
	}
	// Provenance/confidence must come from the stamped TAKES edge, not a hardcode.
	if h.Provenance != "declared" {
		t.Fatalf("want provenance 'declared', got %q", h.Provenance)
	}
	if h.Confidence != declaredConfidence {
		t.Fatalf("want confidence %v, got %v", declaredConfidence, h.Confidence)
	}
}

//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// TestInterfaceImpactByInstanceIndex seeds the two-candidate-path fixture
// (interface:a has instance=10.0.0.1, ifIndex=42) and asserts the exact
// (instance, ifIndex) lookup returns the same impacted app as the phash path.
func TestInterfaceImpactByInstanceIndex(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedTwoCandidatePaths(t, ctx, r)

	rows, err := r.InterfaceImpactByInstanceIndex(ctx, "10.0.0.1", 42, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].App != "payments" {
		t.Fatalf("expected payments impact, got %+v", rows)
	}
}

// TestInterfaceImpactByInstanceIndexNoMatch asserts an unknown (instance,
// ifIndex) returns an empty slice and no error (fail-open friendly).
func TestInterfaceImpactByInstanceIndexNoMatch(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedTwoCandidatePaths(t, ctx, r)

	rows, err := r.InterfaceImpactByInstanceIndex(ctx, "10.9.9.9", 999, time.Now())
	if err != nil {
		t.Fatalf("expected nil error for no match, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty rows for no match, got %+v", rows)
	}
}

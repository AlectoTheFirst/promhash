//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/starkweb/promhash/internal/testutil"
)

func TestInterfaceImpact(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedTwoCandidatePaths(t, ctx, r) // reused helper: payments traverses interface:a
	rows, err := r.InterfaceImpact(ctx, "interface:a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].App == "" {
		t.Fatalf("expected impacted app, got %+v", rows)
	}
}

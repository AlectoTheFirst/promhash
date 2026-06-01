//go:build integration

package graph

import (
	"context"
	"testing"

	"github.com/starkweb/promhash/internal/testutil"
)

func TestEnsureConstraintsIdempotent(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	if err := r.EnsureConstraints(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.EnsureConstraints(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
}

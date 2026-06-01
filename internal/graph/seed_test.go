//go:build integration

package graph

import (
	"context"
	"testing"

	"github.com/starkweb/promhash/internal/testutil"
)

func TestUpsertAppSeed(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	if err := r.UpsertAppSeed(ctx, "application:payments", "payments", "appservice:payments-api", "payments-api", "abc"); err != nil {
		t.Fatal(err)
	}
	apps, _ := r.ListApps(ctx)
	if len(apps) != 1 {
		t.Fatalf("got %v", apps)
	}
}

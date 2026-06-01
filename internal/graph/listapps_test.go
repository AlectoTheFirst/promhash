//go:build integration

package graph

import (
	"context"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestListApps(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	_ = r.write(ctx, `CREATE (:Application {phash:'application:payments', name:'payments'})`, nil)
	apps, err := r.ListApps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 1 || apps[0] != "payments" {
		t.Fatalf("got %v", apps)
	}
}

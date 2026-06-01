//go:build integration

package testutil

import (
	"context"
	"testing"
)

func TestNeo4jContainerConnects(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := Neo4j(t, ctx)
	defer cleanup()
	if err := drv.VerifyConnectivity(ctx); err != nil {
		t.Fatalf("connectivity: %v", err)
	}
}

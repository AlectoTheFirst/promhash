//go:build integration

package testutil

import (
	"context"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	tcneo4j "github.com/testcontainers/testcontainers-go/modules/neo4j"
)

// Neo4j starts a throwaway Neo4j 5 container and returns a connected driver.
func Neo4j(t *testing.T, ctx context.Context) (neo4j.DriverWithContext, func()) {
	t.Helper()
	c, err := tcneo4j.Run(ctx, "neo4j:5.23", tcneo4j.WithAdminPassword("testpass"))
	if err != nil {
		t.Fatalf("start neo4j: %v", err)
	}
	uri, err := c.BoltUrl(ctx)
	if err != nil {
		t.Fatalf("bolt url: %v", err)
	}
	drv, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth("neo4j", "testpass", ""))
	if err != nil {
		t.Fatalf("driver: %v", err)
	}
	return drv, func() { _ = drv.Close(ctx); _ = c.Terminate(ctx) }
}

//go:build integration

package catalog

import (
	"context"
	"testing"

	"github.com/starkweb/promhash/internal/graph"
	"github.com/starkweb/promhash/internal/promclient"
	"github.com/starkweb/promhash/internal/testutil"
)

func TestSyncUpsertsInterfaces(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := graph.New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	rows := []promclient.IfaceRow{{Instance: "10.0.0.1", IfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", IfIndex: 42}}
	devByInstance := map[string]string{"10.0.0.1": "rtr-core-1"}
	if err := Sync(ctx, r, rows, devByInstance, "cisco"); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetInterfaceByPHash(ctx, ifacePHash("rtr-core-1", "tengige0/1/2"))
	if err != nil {
		t.Fatal(err)
	}
	if got.MetricIfName != "Te0/1/2" || got.Device != "rtr-core-1" {
		t.Fatalf("got %+v", got)
	}
}

package declare

import (
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func resolver() *catalog.Resolver {
	return catalog.NewResolver([]graph.Iface{
		{PHash: "interface:1", Device: "rtr-acc-fra-1", IfName: "tengige0/1/2", MetricIfName: "Te0/1/2", Vendor: "cisco"},
		{PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/2/1", MetricIfName: "Te0/2/1", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
	})
}

func TestValidateOK(t *testing.T) {
	a, _ := Parse([]byte(sample))
	if errs := Validate(a, resolver()); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

func TestValidateUnknownInterfaceFails(t *testing.T) {
	a, _ := Parse([]byte(sample))
	a.DependsOn[0].Paths[0].Hops[0].If = "Te9/9/9"
	errs := Validate(a, resolver())
	if len(errs) == 0 {
		t.Fatal("expected a validation error for unknown interface")
	}
}

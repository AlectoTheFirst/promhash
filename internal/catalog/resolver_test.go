package catalog

import (
	"errors"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

func cat() []graph.Iface {
	return []graph.Iface{
		{PHash: "interface:1", Device: "rtr-core-1", IfName: "tengige0/1/2",
			MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
		{PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/1/3",
			MetricIfName: "Te0/1/3", IfDescr: "TenGigE0/1/3", IfAlias: "uplink-auth-dc", Vendor: "cisco"},
	}
}

func TestResolveByName(t *testing.T) {
	r := NewResolver(cat())
	got, err := r.Resolve("rtr-core-1", "Te0/1/2")
	if err != nil {
		t.Fatal(err)
	}
	if got.PHash != "interface:1" {
		t.Fatalf("got %s", got.PHash)
	}
}

func TestResolveByAlias(t *testing.T) {
	r := NewResolver(cat())
	got, err := r.Resolve("rtr-core-1", "uplink-ledger-dc")
	if err != nil {
		t.Fatal(err)
	}
	if got.PHash != "interface:1" {
		t.Fatalf("got %s", got.PHash)
	}
}

func TestResolveNoMatchFailsLoud(t *testing.T) {
	r := NewResolver(cat())
	_, err := r.Resolve("rtr-core-1", "Te9/9/9")
	var nm *NoMatchError
	if !errorsAs(err, &nm) {
		t.Fatalf("want NoMatchError, got %v", err)
	}
	if len(nm.Suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
}

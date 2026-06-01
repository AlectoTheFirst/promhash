package enrich

import (
	"os"
	"testing"

	"github.com/starkweb/promhash/internal/graph"
)

func TestRuleGroupGolden(t *testing.T) {
	hops := []graph.Hop{
		{Device: "rtr-acc-fra-1", MetricIfName: "Te0/1/2", Instance: "10.0.0.5", IfIndex: 7, Direction: "egress"},
		{Device: "rtr-dc-ledger", MetricIfName: "Te1/0/4", Instance: "10.0.0.1", IfIndex: 42, Direction: "ingress"},
	}
	got := RuleGroup("payments", "payments-api", hops)
	want, _ := os.ReadFile("testdata/payments.golden.yaml")
	if got != string(want) {
		t.Fatalf("golden mismatch:\n%s", got)
	}
}

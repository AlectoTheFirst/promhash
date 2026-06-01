package enrich

import (
	"os"
	"strings"
	"testing"

	"github.com/starkweb/promhash/internal/graph"
)

func TestRuleGroupGolden(t *testing.T) {
	hops := []graph.Hop{
		{Device: "rtr-acc-fra-1", MetricIfName: "Te0/1/2", Instance: "10.0.0.5", IfIndex: 7, Direction: "egress"},
		{Device: "rtr-core-1", MetricIfName: "Te0/2/0", Instance: "10.0.0.3", IfIndex: 9, Direction: "transit"},
		{Device: "rtr-dc-ledger", MetricIfName: "Te1/0/4", Instance: "10.0.0.1", IfIndex: 42, Direction: "ingress"},
	}
	got := RuleGroup("payments", "payments-api", hops)
	want, err := os.ReadFile("testdata/payments.golden.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch:\n got:\n%s\nwant:\n%s", got, string(want))
	}
}

// TestRuleGroupTransitEmitsBothDirections asserts that a single transit hop
// produces both an ingress (ifHCInOctets) and an egress (ifHCOutOctets) rule.
func TestRuleGroupTransitEmitsBothDirections(t *testing.T) {
	hops := []graph.Hop{
		{Device: "rtr-core-1", MetricIfName: "Te0/2/0", Instance: "10.0.0.3", IfIndex: 9, Direction: "transit"},
	}
	got := RuleGroup("payments", "payments-api", hops)
	wantIn := `  - record: app:if_ingress_octets:rate5m
    expr: rate(ifHCInOctets{job="promhash-fed-payments", instance="10.0.0.3", ifIndex="9"}[5m])
    labels: {app: payments, service: payments-api, device: rtr-core-1, ifName: Te0/2/0, coverage: declared}
`
	wantOut := `  - record: app:if_egress_octets:rate5m
    expr: rate(ifHCOutOctets{job="promhash-fed-payments", instance="10.0.0.3", ifIndex="9"}[5m])
    labels: {app: payments, service: payments-api, device: rtr-core-1, ifName: Te0/2/0, coverage: declared}
`
	if !strings.Contains(got, wantIn) {
		t.Errorf("transit hop missing ingress rule:\n got:\n%s\nwant substring:\n%s", got, wantIn)
	}
	if !strings.Contains(got, wantOut) {
		t.Errorf("transit hop missing egress rule:\n got:\n%s\nwant substring:\n%s", got, wantOut)
	}
}

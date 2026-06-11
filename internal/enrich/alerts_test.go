package enrich

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// unmarshalAlerts parses PathHealthAlerts output into its document shape.
func unmarshalAlerts(t *testing.T, jk JoinKey) alertGroupsDoc {
	t.Helper()
	var doc alertGroupsDoc
	if err := yaml.Unmarshal([]byte(PathHealthAlerts(jk)), &doc); err != nil {
		t.Fatalf("unmarshal PathHealthAlerts: %v", err)
	}
	return doc
}

// alertExprFor returns the expr of the named alert.
func alertExprFor(t *testing.T, jk JoinKey, name string) string {
	t.Helper()
	doc := unmarshalAlerts(t, jk)
	for _, r := range doc.Groups[0].Rules {
		if r.Alert == name {
			return r.Expr
		}
	}
	t.Fatalf("alert %q not found in PathHealthAlerts(%v)", name, jk)
	return ""
}

// TestPathHealthAlerts_Shape: valid single-group document, expected group name,
// and the full alert set — pipeline meta-alerts first, then path alerts. Every
// rule must carry a severity label and a summary annotation.
func TestPathHealthAlerts_Shape(t *testing.T) {
	doc := unmarshalAlerts(t, JoinByIfName)
	if len(doc.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(doc.Groups))
	}
	if doc.Groups[0].Name != "promhash_path_health_alerts" {
		t.Errorf("group name = %q, want promhash_path_health_alerts", doc.Groups[0].Name)
	}

	wantAlerts := []string{
		"PromhashMappingAbsent",
		"PromhashMappingScrapeDown",
		"PromhashCountersStale",
		"PromhashMappingDrift",
		"PromhashPathHopDown",
		"PromhashPathUtilizationHigh",
		"PromhashPathErrors",
	}
	if len(doc.Groups[0].Rules) != len(wantAlerts) {
		t.Fatalf("got %d alerts, want %d", len(doc.Groups[0].Rules), len(wantAlerts))
	}
	for i, want := range wantAlerts {
		r := doc.Groups[0].Rules[i]
		if r.Alert != want {
			t.Errorf("alert[%d] = %q, want %q", i, r.Alert, want)
		}
		if r.Labels["severity"] == "" {
			t.Errorf("alert %q missing severity label", r.Alert)
		}
		if r.Annotations["summary"] == "" {
			t.Errorf("alert %q missing summary annotation", r.Alert)
		}
	}
}

// TestPathHealthAlerts_DriftExprFollowsJoinKey: the mapping-drift alert must
// compare mapping and counters with the SAME join clause as the recording
// rules it guards.
func TestPathHealthAlerts_DriftExprFollowsJoinKey(t *testing.T) {
	composite := alertExprFor(t, JoinByComposite, "PromhashMappingDrift")
	if !strings.Contains(composite, "unless on(iface)") {
		t.Errorf("composite drift expr must use unless on(iface): %q", composite)
	}
	ifname := alertExprFor(t, JoinByIfName, "PromhashMappingDrift")
	if !strings.Contains(ifname, "unless on(instance, ifName)") {
		t.Errorf("ifname drift expr must use unless on(instance, ifName): %q", ifname)
	}
}

// TestPathHealthAlerts_DriftExprDetectsOrphanMapping evaluates the drift expr:
// one mapping row with a matching counter, one without → the expression fires
// with value 1 (one orphaned row, counting per-direction duplicates once each).
func TestPathHealthAlerts_DriftExprDetectsOrphanMapping(t *testing.T) {
	const load = `load 1m
ifHCInOctets{instance="10.0.0.1", ifName="Te0/1"} 0+1000x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", direction="egress"} 1+0x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te9/9", instance="10.0.0.9", direction="egress"} 1+0x6
`
	expr := alertExprFor(t, JoinByIfName, "PromhashMappingDrift")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected drift expr to yield 1 result, got %d: %v", len(vec), vec)
	}
	if got := vec[0].F; got != 1 {
		t.Errorf("drift count = %v, want 1 (exactly one orphaned mapping row)", got)
	}
}

// TestPathHealthAlerts_DriftExprQuietWhenAligned: all mapping rows joined by a
// counter → the `> 0` filter yields no result, the alert stays silent.
func TestPathHealthAlerts_DriftExprQuietWhenAligned(t *testing.T) {
	const load = `load 1m
ifHCInOctets{instance="10.0.0.1", ifName="Te0/1"} 0+1000x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", direction="egress"} 1+0x6
`
	expr := alertExprFor(t, JoinByIfName, "PromhashMappingDrift")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 0 {
		t.Fatalf("expected no drift result when mapping and counters align, got %d: %v", len(vec), vec)
	}
}

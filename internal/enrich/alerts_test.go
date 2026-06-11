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
		"PromhashCountersAbsent",
		"PromhashCountersStale",
		"PromhashMappingDrift",
		"PromhashPathHopDown",
		"PromhashPathUtilizationHigh",
		"PromhashPathErrors",
		"PromhashPathDiscards",
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

// TestPathHealthAlerts_CountersAbsentFiresOnColdStart: with NO ifHCInOctets
// ever ingested (e.g. a misconfigured remote_write URL on day one), the
// counters-absent expression must yield a result. PromhashCountersStale's
// timestamp() expression evaluates to empty in that state, so without this
// alert a never-started feed would only surface via the 30-minute warning
// drift alert.
func TestPathHealthAlerts_CountersAbsentFiresOnColdStart(t *testing.T) {
	const load = `load 1m
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", direction="egress"} 1+0x6
`
	expr := alertExprFor(t, JoinByIfName, "PromhashCountersAbsent")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected counters-absent expr to yield 1 result on cold start, got %d: %v", len(vec), vec)
	}
	if got := vec[0].F; got != 1 {
		t.Errorf("counters-absent value = %v, want 1", got)
	}
}

// TestPathHealthAlerts_CountersAbsentQuietWhenCountersPresent: any ifHCInOctets
// sample silences the absent() expression (staleness is PromhashCountersStale's
// job, not this alert's).
func TestPathHealthAlerts_CountersAbsentQuietWhenCountersPresent(t *testing.T) {
	const load = `load 1m
ifHCInOctets{instance="10.0.0.1", ifName="Te0/1"} 0+1000x6
`
	expr := alertExprFor(t, JoinByIfName, "PromhashCountersAbsent")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 0 {
		t.Fatalf("expected no counters-absent result while counters flow, got %d: %v", len(vec), vec)
	}
}

// TestPathHealthAlerts_CountersAbsentSeverity: cold-start silence is the
// pipeline's signature failure mode, so the alert must page (critical).
func TestPathHealthAlerts_CountersAbsentSeverity(t *testing.T) {
	doc := unmarshalAlerts(t, JoinByIfName)
	for _, r := range doc.Groups[0].Rules {
		if r.Alert == "PromhashCountersAbsent" {
			if r.Labels["severity"] != "critical" {
				t.Errorf("PromhashCountersAbsent severity = %q, want critical", r.Labels["severity"])
			}
			return
		}
	}
	t.Fatal("PromhashCountersAbsent not found")
}

// TestPathHealthAlerts_PathDiscardsFiresOnNonZeroRate: the discards rollup has
// a recording rule, so it must also have the matching alert (mirroring
// PromhashPathErrors).
func TestPathHealthAlerts_PathDiscardsFiresOnNonZeroRate(t *testing.T) {
	const load = `load 1m
app:path_discards:rate5m{app="payments", service="svc"} 5+0x6
`
	expr := alertExprFor(t, JoinByIfName, "PromhashPathDiscards")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected discards expr to yield 1 result, got %d: %v", len(vec), vec)
	}
	if got := vec[0].F; got != 5 {
		t.Errorf("discards value = %v, want 5", got)
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

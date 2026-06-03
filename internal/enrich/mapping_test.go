package enrich

import (
	"sort"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// helper: collect distinct direction strings from a slice of MappingPoints.
func directionSet(pts []MappingPoint) map[string]struct{} {
	s := map[string]struct{}{}
	for _, p := range pts {
		s[p.Direction] = struct{}{}
	}
	return s
}

// Test 1 – Transit dedup across candidate paths.
//
// A transit hop for (instance=10.0.0.1, ifIndex=7) appears at two different
// Seq values, simulating the same interface appearing on two candidate paths.
// MappingSeries must return exactly 2 points (ingress + egress), NOT 4.
func TestMappingSeries_TransitDedupAcrossCandidatePaths(t *testing.T) {
	hops := []graph.Hop{
		{Instance: "10.0.0.1", IfIndex: 7, MetricIfName: "eth0", Device: "sw1", Direction: "transit", Seq: 1},
		{Instance: "10.0.0.1", IfIndex: 7, MetricIfName: "eth0", Device: "sw1", Direction: "transit", Seq: 2},
	}

	pts := MappingSeries("payments", "svc", hops, JoinByComposite)

	if len(pts) != 2 {
		t.Fatalf("expected 2 points (deduped ingress+egress), got %d: %+v", len(pts), pts)
	}

	dirs := directionSet(pts)
	if _, ok := dirs["ingress"]; !ok {
		t.Errorf("missing ingress point; got directions: %v", dirs)
	}
	if _, ok := dirs["egress"]; !ok {
		t.Errorf("missing egress point; got directions: %v", dirs)
	}
}

// Test 2 – RenderMappingSeries produces valid Prometheus exposition text.
//
// Parse with expfmt.TextParser; assert metric family is promhash_interface_app,
// every sample value == 1, and the app label round-trips correctly.
func TestRenderMappingSeries_ParsesAndValueIsOne(t *testing.T) {
	hops := []graph.Hop{
		{Instance: "10.0.0.1", IfIndex: 42, MetricIfName: "GigE0/1", Device: "rtr1", Direction: "ingress", Seq: 1},
	}

	pts := MappingSeries("payments", "paysvc", hops, JoinByIfName)
	if len(pts) == 0 {
		t.Fatal("expected at least one point")
	}

	rendered := RenderMappingSeries(pts)

	parser := expfmt.NewTextParser(model.LegacyValidation)
	mf, err := parser.TextToMetricFamilies(strings.NewReader(rendered))
	if err != nil {
		t.Fatalf("parse error: %v\nrendered:\n%s", err, rendered)
	}

	family, ok := mf["promhash_interface_app"]
	if !ok {
		t.Fatalf("metric family 'promhash_interface_app' not found; got families: %v\nrendered:\n%s", mfKeys(mf), rendered)
	}

	for _, m := range family.Metric {
		// RenderMappingSeries emits no # TYPE header, so the parser treats the
		// metric as untyped. Check the untyped value field.
		v := m.GetUntyped().GetValue()
		if v != 1.0 {
			t.Errorf("expected value 1, got %v for metric %v", v, m)
		}
		// Assert app label round-trips.
		found := false
		for _, lp := range m.Label {
			if lp.GetName() == "app" && lp.GetValue() == "payments" {
				found = true
			}
		}
		if !found {
			t.Errorf("app label 'payments' not found; labels: %v", m.Label)
		}
	}
}

// Test 3 – Shared core link, 2 apps → 2 series differing only in `app`.
//
// The same transit hop rendered under "payments" and "ledger" produces 4 points
// total (2 directions × 2 apps). For the shared iface the two series differ
// only in the app label. This is the group_left fan-out precondition.
func TestMappingSeries_TwoAppsShareIface(t *testing.T) {
	sharedHop := graph.Hop{
		Instance: "10.0.0.1", IfIndex: 7, MetricIfName: "eth1", Device: "core1",
		Direction: "transit", Seq: 1,
	}

	ptsPay := MappingSeries("payments", "pay-svc", []graph.Hop{sharedHop}, JoinByComposite)
	ptsLed := MappingSeries("ledger", "led-svc", []graph.Hop{sharedHop}, JoinByComposite)

	allPts := append(ptsPay, ptsLed...)
	rendered := RenderMappingSeries(allPts)

	parser := expfmt.NewTextParser(model.LegacyValidation)
	mf, err := parser.TextToMetricFamilies(strings.NewReader(rendered))
	if err != nil {
		t.Fatalf("parse error: %v\nrendered:\n%s", err, rendered)
	}

	family, ok := mf["promhash_interface_app"]
	if !ok {
		t.Fatalf("metric family 'promhash_interface_app' not found\nrendered:\n%s", rendered)
	}

	// Find metrics for iface "10.0.0.1:7".
	var forIface []labelMap
	for _, m := range family.Metric {
		lm := toLabelMap(m.Label)
		if lm["iface"] == "10.0.0.1:7" {
			forIface = append(forIface, lm)
		}
	}

	// 2 directions × 2 apps = 4 points total for this iface.
	if len(forIface) != 4 {
		t.Fatalf("expected 4 series for iface 10.0.0.1:7, got %d\nrendered:\n%s", len(forIface), rendered)
	}

	// Collect the distinct app values.
	appSet := map[string]struct{}{}
	for _, lm := range forIface {
		appSet[lm["app"]] = struct{}{}
	}
	if _, ok := appSet["payments"]; !ok {
		t.Errorf("missing app=payments series; apps present: %v", appSet)
	}
	if _, ok := appSet["ledger"]; !ok {
		t.Errorf("missing app=ledger series; apps present: %v", appSet)
	}

	// Every pair of series for the same (iface, direction, app) should be
	// identical except for the app label: verify the non-app labels match
	// across the two apps for a given direction.
	dirPayLabels := map[string]labelMap{}
	dirLedLabels := map[string]labelMap{}
	for _, lm := range forIface {
		switch lm["app"] {
		case "payments":
			dirPayLabels[lm["direction"]] = lm
		case "ledger":
			dirLedLabels[lm["direction"]] = lm
		}
	}
	for dir, payLM := range dirPayLabels {
		ledLM, ok := dirLedLabels[dir]
		if !ok {
			t.Errorf("ledger has no series for direction %q", dir)
			continue
		}
		for _, k := range []string{"iface", "instance", "ifIndex", "ifName", "device", "direction"} {
			if payLM[k] != ledLM[k] {
				t.Errorf("label %q differs: payments=%q ledger=%q", k, payLM[k], ledLM[k])
			}
		}
	}
}

// Test 4 – Distinct iface for same ifIndex on different instances.
//
// Two hops both have ifIndex=42 but different instances. The iface label must
// be distinct: "10.0.0.1:42" and "10.0.0.2:42".
func TestMappingSeries_DistinctIfaceForSameIfIndexDifferentInstances(t *testing.T) {
	hops := []graph.Hop{
		{Instance: "10.0.0.1", IfIndex: 42, MetricIfName: "eth0", Device: "sw1", Direction: "ingress", Seq: 1},
		{Instance: "10.0.0.2", IfIndex: 42, MetricIfName: "eth0", Device: "sw2", Direction: "ingress", Seq: 2},
	}

	pts := MappingSeries("payments", "svc", hops, JoinByComposite)

	if len(pts) != 2 {
		t.Fatalf("expected 2 points, got %d: %+v", len(pts), pts)
	}

	ifaceSet := map[string]struct{}{}
	for _, p := range pts {
		ifaceSet[p.Iface] = struct{}{}
	}

	if _, ok := ifaceSet["10.0.0.1:42"]; !ok {
		t.Errorf("missing iface 10.0.0.1:42; got %v", ifaceSet)
	}
	if _, ok := ifaceSet["10.0.0.2:42"]; !ok {
		t.Errorf("missing iface 10.0.0.2:42; got %v", ifaceSet)
	}
}

// --- helpers ---

type labelMap map[string]string

func toLabelMap(pairs []*io_prometheus_client.LabelPair) labelMap {
	lm := labelMap{}
	for _, p := range pairs {
		lm[p.GetName()] = p.GetValue()
	}
	return lm
}

func mfKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

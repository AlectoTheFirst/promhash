package enrich

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/promql/promqltest"

	"gopkg.in/yaml.v3"
)

// testStartTime mirrors promqltest's internal epoch: series loaded via a
// `load <step>` block place their first sample at Unix(0,0) UTC, with each
// subsequent sample advancing by one step. Query eval times are computed
// relative to this so we evaluate at the right offset into the loaded data.
var testStartTime = time.Unix(0, 0).UTC()

// evalExpr loads the given promqltest `load` fixture block, evaluates expr as an
// instant query at evalTime, and returns the resulting vector. It fails the test
// on any load/parse/exec error. The engine uses generous sample/lookback limits
// so fixtures need not worry about defaults.
func evalExpr(t *testing.T, loadBlock, expr string, evalTime time.Time) promql.Vector {
	t.Helper()

	storage := promqltest.LoadedStorage(t, loadBlock)
	t.Cleanup(func() { storage.Close() })

	// 5m lookback so a counter sample at t=0 is still in range when we evaluate
	// rate(...) at t=5m; large maxSamples since these fixtures are tiny.
	engine := promqltest.NewTestEngine(t, false, 5*time.Minute, promqltest.DefaultMaxSamplesPerQuery)

	ctx := context.Background()
	q, err := engine.NewInstantQuery(ctx, storage, nil, expr, evalTime)
	if err != nil {
		t.Fatalf("NewInstantQuery(%q): %v", expr, err)
	}
	defer q.Close()

	res := q.Exec(ctx)
	if res.Err != nil {
		t.Fatalf("exec %q: %v", expr, res.Err)
	}

	vec, ok := res.Value.(promql.Vector)
	if !ok {
		t.Fatalf("expr %q: result is %T, want promql.Vector", expr, res.Value)
	}
	return vec
}

// exprFor recovers the rendered expr for a given record name out of the YAML
// produced by PathHealthRules(jk). This guarantees the behavioral tests exercise
// the ACTUAL rendered expr rather than a hand-retyped copy: any drift in the
// generator immediately changes what the tests run.
func exprFor(t *testing.T, jk JoinKey, record string) string {
	t.Helper()

	var doc ruleGroupsDoc
	if err := yaml.Unmarshal([]byte(PathHealthRules(jk)), &doc); err != nil {
		t.Fatalf("unmarshal PathHealthRules: %v", err)
	}
	if len(doc.Groups) != 1 {
		t.Fatalf("expected exactly 1 group, got %d", len(doc.Groups))
	}
	for _, r := range doc.Groups[0].Rules {
		if r.Record == record {
			return r.Expr
		}
	}
	t.Fatalf("record %q not found in PathHealthRules(%v)", record, jk)
	return ""
}

// labelMapFromLabels flattens a labels.Labels into a plain map for assertions.
func labelMapFromLabels(ls labels.Labels) map[string]string {
	m := map[string]string{}
	ls.Range(func(l labels.Label) {
		m[l.Name] = l.Value
	})
	return m
}

// at returns the test epoch advanced by d.
func at(d time.Duration) time.Time { return testStartTime.Add(d) }

// --- Structural tests over the rendered YAML ---

// TestPathHealthRules_UnmarshalsAndShape asserts the rendered YAML is a valid
// single-group rules document with the expected group name and the full set of
// record names in order.
func TestPathHealthRules_UnmarshalsAndShape(t *testing.T) {
	var doc ruleGroupsDoc
	if err := yaml.Unmarshal([]byte(PathHealthRules(JoinByComposite)), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(doc.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(doc.Groups))
	}
	if doc.Groups[0].Name != "promhash_path_health" {
		t.Errorf("group name = %q, want promhash_path_health", doc.Groups[0].Name)
	}

	wantRecords := []string{
		"app:if_egress_octets:rate5m",
		"app:if_ingress_octets:rate5m",
		"app:if_capacity_bps",
		"app:if_oper_up:state",
		"app:if_util:ratio",
		"app:path_util_max:ratio",
		"app:path_oper_up_min:state",
		"app:path_hops_down:count",
	}
	if len(doc.Groups[0].Rules) != len(wantRecords) {
		t.Fatalf("got %d rules, want %d", len(doc.Groups[0].Rules), len(wantRecords))
	}
	for i, want := range wantRecords {
		if got := doc.Groups[0].Rules[i].Record; got != want {
			t.Errorf("rule[%d].record = %q, want %q", i, got, want)
		}
	}
}

// TestPathHealthRules_JoinKeys asserts the on(...) clause and group_left label
// set differ correctly per JoinKey, and that rollups use the right aggregators
// grouped by(app, service). This is the structural guard that survives even if
// the PromQL eval harness is ever removed.
func TestPathHealthRules_JoinKeys(t *testing.T) {
	composite := PathHealthRules(JoinByComposite)
	ifname := PathHealthRules(JoinByIfName)

	if !strings.Contains(composite, "on(iface)") {
		t.Errorf("JoinByComposite missing on(iface)\n%s", composite)
	}
	if strings.Contains(composite, "on(instance, ifName)") {
		t.Errorf("JoinByComposite must not contain on(instance, ifName)\n%s", composite)
	}
	if !strings.Contains(ifname, "on(instance, ifName)") {
		t.Errorf("JoinByIfName missing on(instance, ifName)\n%s", ifname)
	}
	if strings.Contains(ifname, "on(iface)") {
		t.Errorf("JoinByIfName must not contain on(iface)\n%s", ifname)
	}

	// group_right() fan-out: the mapping is the many side, so the empty
	// group_right() clause is identical regardless of join key (and, being
	// empty, keeps the join-key labels out of the GROUP clause).
	for _, y := range []string{composite, ifname} {
		if !strings.Contains(y, "group_right()") {
			t.Errorf("missing group_right() fan-out clause\n%s", y)
		}
	}

	// Rollups: MAX for util, MIN for oper-up, COUNT for hops-down, all by(app, service).
	// hops-down uses `== 0` as a FILTER (no bool), so only down hops are counted.
	checks := []string{
		"max by(app, service)(app:if_util:ratio)",
		"min by(app, service)(app:if_oper_up:state)",
		"count by(app, service)(app:if_oper_up:state == 0)",
	}
	for _, want := range checks {
		if !strings.Contains(composite, want) {
			t.Errorf("missing rollup expr %q\n%s", want, composite)
		}
	}
}

// --- Behavioral PromQL-evaluation tests (JoinByComposite / on(iface)) ---

// Test 1: egress join carries app/service/device/ifName labels onto a nonzero
// rate. One counter, one matching mapping series → exactly one output series.
func TestPathHealth_EgressJoinCarriesLabelsAndRate(t *testing.T) {
	const load = `load 1m
ifHCOutOctets{instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", ifName="Te0/1"} 0+1000x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="egress"} 1+0x6
`
	expr := exprFor(t, JoinByComposite, "app:if_egress_octets:rate5m")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected exactly 1 series, got %d: %v", len(vec), vec)
	}
	s := vec[0]
	lm := labelMapFromLabels(s.Metric)
	for k, want := range map[string]string{
		"app":     "payments",
		"service": "svc",
		"device":  "rtr1",
		"ifName":  "Te0/1",
	} {
		if lm[k] != want {
			t.Errorf("label %q = %q, want %q (labels: %v)", k, lm[k], want, lm)
		}
	}
	if s.F <= 0 {
		t.Errorf("rate = %v, want > 0", s.F)
	}
}

// Test 2: a counter for an iface with NO matching promhash_interface_app series
// drops out entirely — the inner join removes unmapped counters. This is the
// cardinality-safety guarantee: raw firehose series never reach the output
// unless explicitly mapped.
func TestPathHealth_UnmappedIfaceDropsOut(t *testing.T) {
	const load = `load 1m
ifHCOutOctets{instance="10.0.0.9", ifIndex="99", iface="10.0.0.9:99", ifName="Te9/9"} 0+1000x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="egress"} 1+0x6
`
	expr := exprFor(t, JoinByComposite, "app:if_egress_octets:rate5m")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 0 {
		t.Fatalf("expected 0 series for unmapped iface, got %d: %v", len(vec), vec)
	}
}

// Test 3: a shared physical link mapped to TWO apps fans out to two output
// series, one per app — group_left's many-to-one fan-out. The two mapping
// series differ only in app/service.
func TestPathHealth_SharedLinkFansOutToTwoApps(t *testing.T) {
	const load = `load 1m
ifHCOutOctets{instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", ifName="Te0/1"} 0+1000x6
promhash_interface_app{app="payments", service="pay", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="egress"} 1+0x6
promhash_interface_app{app="ledger", service="led", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="egress"} 1+0x6
`
	expr := exprFor(t, JoinByComposite, "app:if_egress_octets:rate5m")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 2 {
		t.Fatalf("expected 2 series (one per app), got %d: %v", len(vec), vec)
	}
	apps := map[string]struct{}{}
	for _, s := range vec {
		lm := labelMapFromLabels(s.Metric)
		apps[lm["app"]] = struct{}{}
		if s.F <= 0 {
			t.Errorf("series for app=%q has rate %v, want > 0", lm["app"], s.F)
		}
	}
	if _, ok := apps["payments"]; !ok {
		t.Errorf("missing app=payments; got %v", apps)
	}
	if _, ok := apps["ledger"]; !ok {
		t.Errorf("missing app=ledger; got %v", apps)
	}
}

// Test 4: the path rollup is MAX, not sum/avg. Two if_util:ratio series under
// the same (app, service) valued 0.3 and 0.8 collapse to a single 0.8.
// We load the intermediate series directly to avoid chaining rule evaluation.
func TestPathHealth_PathUtilMaxIsMaxNotSum(t *testing.T) {
	const load = `load 1m
app:if_util:ratio{app="payments", service="svc", iface="10.0.0.1:7"} 0.3+0x6
app:if_util:ratio{app="payments", service="svc", iface="10.0.0.1:8"} 0.8+0x6
`
	expr := exprFor(t, JoinByComposite, "app:path_util_max:ratio")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected 1 rolled-up series, got %d: %v", len(vec), vec)
	}
	if got := vec[0].F; got != 0.8 {
		t.Errorf("path_util_max = %v, want 0.8 (MAX of {0.3,0.8}; sum would be 1.1, avg 0.55)", got)
	}
	lm := labelMapFromLabels(vec[0].Metric)
	if lm["app"] != "payments" || lm["service"] != "svc" {
		t.Errorf("rolled-up labels = %v, want app=payments service=svc", lm)
	}
	if _, ok := lm["iface"]; ok {
		t.Errorf("iface label should be aggregated away by `by(app, service)`, got %v", lm)
	}
}

// Test 5: hops-down count. Two oper-up states under one (app,service) — one up
// (1), one down (0) — yield a count of exactly 1 down hop.
func TestPathHealth_HopsDownCount(t *testing.T) {
	const load = `load 1m
app:if_oper_up:state{app="payments", service="svc", iface="10.0.0.1:7"} 1+0x6
app:if_oper_up:state{app="payments", service="svc", iface="10.0.0.1:8"} 0+0x6
`
	expr := exprFor(t, JoinByComposite, "app:path_hops_down:count")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 1 {
		t.Fatalf("expected 1 series, got %d: %v", len(vec), vec)
	}
	if got := vec[0].F; got != 1 {
		t.Errorf("hops_down = %v, want 1", got)
	}
}

// Test 5b: a single transit iface emits BOTH an ingress and an egress mapping
// series for the same app. The direction-agnostic oper_up rule must collapse
// that multiplicity to exactly ONE app:if_oper_up:state series per (iface, app),
// so a down physical interface is counted ONCE — not twice — by
// app:path_hops_down:count. This guards the max without(direction)(…) dedup.
func TestPathHealth_OperUpCollapsesDirectionMultiplicity(t *testing.T) {
	const load = `load 1m
ifOperStatus{instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", ifName="Te0/1"} 0+0x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="ingress"} 1+0x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", ifIndex="7", iface="10.0.0.1:7", direction="egress"} 1+0x6
`
	// First: app:if_oper_up:state must yield exactly ONE series for the iface,
	// despite the two-direction mapping. (Value 0 = down.)
	stateExpr := exprFor(t, JoinByComposite, "app:if_oper_up:state")
	stateVec := evalExpr(t, load, stateExpr, at(5*time.Minute))
	if len(stateVec) != 1 {
		t.Fatalf("expected exactly 1 oper_up series (direction collapsed), got %d: %v", len(stateVec), stateVec)
	}
	lm := labelMapFromLabels(stateVec[0].Metric)
	if lm["app"] != "payments" || lm["iface"] != "10.0.0.1:7" {
		t.Errorf("oper_up labels = %v, want app=payments iface=10.0.0.1:7", lm)
	}
	if _, ok := lm["direction"]; ok {
		t.Errorf("direction label should be collapsed away, got %v", lm)
	}
	if stateVec[0].F != 0 {
		t.Errorf("oper_up state = %v, want 0 (down)", stateVec[0].F)
	}

	// Then: feed the (single) state series into the hops-down count and confirm
	// the down iface is counted exactly ONCE. We chain via a direct load of the
	// collapsed state to keep this assertion independent of rule evaluation order.
	const stateLoad = `load 1m
app:if_oper_up:state{app="payments", service="svc", iface="10.0.0.1:7"} 0+0x6
`
	countExpr := exprFor(t, JoinByComposite, "app:path_hops_down:count")
	countVec := evalExpr(t, stateLoad, countExpr, at(5*time.Minute))
	if len(countVec) != 1 {
		t.Fatalf("expected 1 hops_down series, got %d: %v", len(countVec), countVec)
	}
	if got := countVec[0].F; got != 1 {
		t.Errorf("hops_down = %v, want 1 (down iface counted once, not twice)", got)
	}
}

// Test 6 (smoke): util ratio is capacity-gated. With egress rate present but
// no capacity series (ifHighSpeed absent), if_util:ratio yields NO series
// rather than +Inf. We load the egress rate directly and an empty capacity to
// confirm the division produces nothing instead of an infinity.
func TestPathHealth_UtilHasNoSeriesWithoutCapacity(t *testing.T) {
	const load = `load 1m
app:if_egress_octets:rate5m{app="payments", service="svc", iface="10.0.0.1:7"} 125+0x6
`
	expr := exprFor(t, JoinByComposite, "app:if_util:ratio")
	vec := evalExpr(t, load, expr, at(5*time.Minute))

	if len(vec) != 0 {
		t.Fatalf("expected 0 util series when capacity absent (no div-by-zero +Inf), got %d: %v", len(vec), vec)
	}
}

// TestPathHealth_JoinByIfNameRendersInstanceIfName is a smoke test that the
// JoinByIfName variant joins on(instance, ifName) and still carries the labels.
func TestPathHealth_JoinByIfNameRendersInstanceIfName(t *testing.T) {
	expr := exprFor(t, JoinByIfName, "app:if_egress_octets:rate5m")
	if !strings.Contains(expr, "on(instance, ifName)") {
		t.Fatalf("JoinByIfName egress expr missing on(instance, ifName): %q", expr)
	}

	const load = `load 1m
ifHCOutOctets{instance="10.0.0.1", ifName="Te0/1"} 0+1000x6
promhash_interface_app{app="payments", service="svc", device="rtr1", ifName="Te0/1", instance="10.0.0.1", direction="egress"} 1+0x6
`
	vec := evalExpr(t, load, expr, at(5*time.Minute))
	if len(vec) != 1 {
		t.Fatalf("expected 1 series, got %d: %v", len(vec), vec)
	}
	lm := labelMapFromLabels(vec[0].Metric)
	if lm["app"] != "payments" || lm["ifName"] != "Te0/1" {
		t.Errorf("labels = %v, want app=payments ifName=Te0/1", lm)
	}
}

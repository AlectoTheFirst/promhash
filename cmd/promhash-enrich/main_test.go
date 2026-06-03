package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"gopkg.in/yaml.v3"

	"github.com/AlectoTheFirst/promhash/internal/enrich"
	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// testOpts returns a populated EvaluatorOpts for use in tests.
func testOpts(jk enrich.JoinKey) enrich.EvaluatorOpts {
	return enrich.EvaluatorOpts{
		ScrapeTarget:   "raw-counters:9116",
		RemoteWriteURL: "http://mimir:9090/api/v1/push",
		TenantLabel:    "test-tenant",
		JoinKey:        jk,
	}
}

// sharedHop is a transit hop shared by two apps in the multi-app tests.
var sharedHop = graph.Hop{
	Instance:     "10.0.0.1",
	IfIndex:      7,
	MetricIfName: "eth1",
	Device:       "core1",
	Direction:    "transit",
	Seq:          1,
}

// buildTwoAppInput returns apps and svcNames for "payments" and "ledger",
// both using the sharedHop. No Neo4j required.
func buildTwoAppInput() (apps map[string][]graph.Hop, svcNames map[string]string) {
	apps = map[string][]graph.Hop{
		"payments": {sharedHop},
		"ledger":   {sharedHop},
	}
	svcNames = map[string]string{
		"payments": "pay-svc",
		"ledger":   "led-svc",
	}
	return
}

// Test 1: writeSharedArtifacts writes all three files under tmp/_shared/.
func TestWriteSharedArtifacts_AllFilesExist(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()

	if err := writeSharedArtifacts(tmp, apps, svcNames, testOpts(enrich.JoinByComposite)); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	for _, name := range []string{"mapping.prom", "path-health.rules.yaml", "evaluator.yaml"} {
		p := filepath.Join(tmp, "_shared", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist, got: %v", p, err)
		}
	}
}

// Test 2: mapping.prom parses as valid Prometheus exposition text; for the
// shared iface there are exactly 2 series (one per app) per direction,
// differing only in the app label.
func TestWriteSharedArtifacts_MappingSharedHopTwoSeries(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()

	if err := writeSharedArtifacts(tmp, apps, svcNames, testOpts(enrich.JoinByComposite)); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "_shared", "mapping.prom"))
	if err != nil {
		t.Fatalf("read mapping.prom: %v", err)
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)
	mf, err := parser.TextToMetricFamilies(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("parse mapping.prom: %v\ncontent:\n%s", err, raw)
	}

	family, ok := mf["promhash_interface_app"]
	if !ok {
		t.Fatalf("metric family promhash_interface_app not found; got: %v\ncontent:\n%s", mfNames(mf), raw)
	}

	// Collect metrics for the shared iface "10.0.0.1:7".
	var forIface []*io_prometheus_client.Metric
	for _, m := range family.Metric {
		lm := labelPairMap(m.Label)
		if lm["iface"] == "10.0.0.1:7" {
			forIface = append(forIface, m)
		}
	}

	// transit expands to ingress+egress per app → 4 total for this iface.
	// The task says "shared hop → 2 series" but that's per-direction.
	// For ingress: 2 series (payments + ledger). Same for egress.
	// Collect just ingress and verify exactly 2 apps.
	var ingressSeries []*io_prometheus_client.Metric
	for _, m := range forIface {
		lm := labelPairMap(m.Label)
		if lm["direction"] == "ingress" {
			ingressSeries = append(ingressSeries, m)
		}
	}

	if len(ingressSeries) != 2 {
		t.Fatalf("expected 2 ingress series for shared iface, got %d\ncontent:\n%s", len(ingressSeries), raw)
	}

	// They should have app=payments and app=ledger.
	appSet := map[string]struct{}{}
	for _, m := range ingressSeries {
		lm := labelPairMap(m.Label)
		appSet[lm["app"]] = struct{}{}
		// Each value must be 1.
		if v := m.GetUntyped().GetValue(); v != 1.0 {
			t.Errorf("metric value = %v, want 1", v)
		}
	}
	if _, ok := appSet["payments"]; !ok {
		t.Errorf("missing app=payments in ingress series; got apps: %v", appSet)
	}
	if _, ok := appSet["ledger"]; !ok {
		t.Errorf("missing app=ledger in ingress series; got apps: %v", appSet)
	}

	// Verify non-app labels are identical across the two ingress series.
	var labelsA, labelsB map[string]string
	for _, m := range ingressSeries {
		lm := labelPairMap(m.Label)
		if lm["app"] == "payments" {
			labelsA = lm
		} else {
			labelsB = lm
		}
	}
	for _, k := range []string{"iface", "instance", "ifIndex", "ifName", "device", "direction"} {
		if labelsA[k] != labelsB[k] {
			t.Errorf("label %q differs between apps: payments=%q ledger=%q", k, labelsA[k], labelsB[k])
		}
	}
}

// Test 3: path-health.rules.yaml unmarshals to a groups document with a
// group named "promhash_path_health".
func TestWriteSharedArtifacts_PathHealthRulesGroup(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()

	if err := writeSharedArtifacts(tmp, apps, svcNames, testOpts(enrich.JoinByComposite)); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "_shared", "path-health.rules.yaml"))
	if err != nil {
		t.Fatalf("read path-health.rules.yaml: %v", err)
	}

	var doc struct {
		Groups []struct {
			Name string `yaml:"name"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal path-health.rules.yaml: %v\ncontent:\n%s", err, raw)
	}
	if len(doc.Groups) == 0 {
		t.Fatalf("no groups in path-health.rules.yaml\ncontent:\n%s", raw)
	}
	if doc.Groups[0].Name != "promhash_path_health" {
		t.Errorf("group[0].name = %q, want \"promhash_path_health\"", doc.Groups[0].Name)
	}
}

// Test 4: evaluator.yaml unmarshals to exactly one remote_write whose url
// matches opts.RemoteWriteURL, and global.external_labels.tenant ==
// opts.TenantLabel.
func TestWriteSharedArtifacts_EvaluatorYAML(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()
	opts := testOpts(enrich.JoinByComposite)

	if err := writeSharedArtifacts(tmp, apps, svcNames, opts); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(tmp, "_shared", "evaluator.yaml"))
	if err != nil {
		t.Fatalf("read evaluator.yaml: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal evaluator.yaml: %v\ncontent:\n%s", err, raw)
	}

	// Exactly one remote_write with the right URL.
	rwList, ok := doc["remote_write"].([]any)
	if !ok || len(rwList) != 1 {
		t.Fatalf("remote_write: expected list of length 1, got %T %v", doc["remote_write"], doc["remote_write"])
	}
	rwURL, _ := rwList[0].(map[string]any)["url"].(string)
	if rwURL != opts.RemoteWriteURL {
		t.Errorf("remote_write[0].url = %q, want %q", rwURL, opts.RemoteWriteURL)
	}

	// global.external_labels.tenant == TenantLabel.
	global, _ := doc["global"].(map[string]any)
	extLabels, _ := global["external_labels"].(map[string]any)
	tenant, _ := extLabels["tenant"].(string)
	if tenant != opts.TenantLabel {
		t.Errorf("global.external_labels.tenant = %q, want %q", tenant, opts.TenantLabel)
	}
}

// Test 5: no per-app federate.match files are written.
func TestWriteSharedArtifacts_NoPerAppFederateMatch(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()

	if err := writeSharedArtifacts(tmp, apps, svcNames, testOpts(enrich.JoinByComposite)); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	for app := range apps {
		p := filepath.Join(tmp, app, "federate.match")
		if _, err := os.Stat(p); err == nil {
			t.Errorf("unexpected per-app federate.match found at %s", p)
		}
	}
}

// Test 6: -prune-legacy removes legacy per-app files while leaving _shared/ intact.
func TestPruneLegacyArtifacts(t *testing.T) {
	tmp := t.TempDir()
	apps, svcNames := buildTwoAppInput()

	// First write the shared artifacts so _shared/ exists.
	if err := writeSharedArtifacts(tmp, apps, svcNames, testOpts(enrich.JoinByComposite)); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	// Pre-create fake legacy files for "payments".
	paymentsDir := filepath.Join(tmp, "payments")
	if err := os.MkdirAll(paymentsDir, 0o755); err != nil {
		t.Fatalf("mkdir payments: %v", err)
	}
	legacyFiles := []string{"federate.match", "rules.yaml", "scrape.yaml"}
	for _, f := range legacyFiles {
		p := filepath.Join(paymentsDir, f)
		if err := os.WriteFile(p, []byte("legacy"), 0o644); err != nil {
			t.Fatalf("write legacy %s: %v", f, err)
		}
	}

	// Run prune.
	if err := pruneLegacyArtifacts(tmp, apps); err != nil {
		t.Fatalf("pruneLegacyArtifacts: %v", err)
	}

	// Legacy files must be gone.
	for _, f := range legacyFiles {
		p := filepath.Join(paymentsDir, f)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("expected %s to be pruned, but it still exists", p)
		}
	}

	// _shared/ must still contain all three artifacts.
	for _, name := range []string{"mapping.prom", "path-health.rules.yaml", "evaluator.yaml"} {
		p := filepath.Join(tmp, "_shared", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("_shared/%s unexpectedly missing after prune: %v", name, err)
		}
	}
}

// Test 7: pruneLegacyArtifacts skips app names that would traverse outside outDir.
func TestPruneLegacyArtifacts_RejectsTraversalAppName(t *testing.T) {
	// outDir is the directory pruneLegacyArtifacts is told to operate on.
	outDir := t.TempDir()
	// siblingDir simulates a directory OUTSIDE outDir that would be reached by
	// an unguarded filepath.Join(outDir, "../evil").
	siblingDir := t.TempDir()

	// Plant a legacy file in the sibling dir that the unguarded code would remove.
	sentinel := filepath.Join(siblingDir, "federate.match")
	if err := os.WriteFile(sentinel, []byte("should not be removed"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	// Construct the traversal app name so that filepath.Join(outDir, app) points
	// at siblingDir. We need a relative path from outDir to siblingDir.
	rel, err := filepath.Rel(outDir, siblingDir)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}

	apps := map[string][]graph.Hop{
		rel: {sharedHop},
	}

	if err := pruneLegacyArtifacts(outDir, apps); err != nil {
		t.Fatalf("pruneLegacyArtifacts: %v", err)
	}

	// The sentinel file outside outDir must still exist.
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel file outside outDir was removed or made inaccessible: %v", err)
	}
}

// Test 9: parseJoinKey rejects unknown values.
func TestParseJoinKey_UnknownValueReturnsError(t *testing.T) {
	_, err := parseJoinKey("bogus")
	if err == nil {
		t.Error("expected error for unknown join-key value, got nil")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("error should mention the bad value; got: %v", err)
	}
}

// Test 10: parseJoinKey maps known values correctly.
func TestParseJoinKey_KnownValues(t *testing.T) {
	cases := []struct {
		in   string
		want enrich.JoinKey
	}{
		{"composite", enrich.JoinByComposite},
		{"ifname", enrich.JoinByIfName},
		{"COMPOSITE", enrich.JoinByComposite},
		{"IFNAME", enrich.JoinByIfName},
	}
	for _, c := range cases {
		got, err := parseJoinKey(c.in)
		if err != nil {
			t.Errorf("parseJoinKey(%q): unexpected error: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("parseJoinKey(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// --- helpers ---

func labelPairMap(pairs []*io_prometheus_client.LabelPair) map[string]string {
	m := map[string]string{}
	for _, p := range pairs {
		m[p.GetName()] = p.GetValue()
	}
	return m
}

func mfNames[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

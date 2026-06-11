package enrich

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// evalCfgOpts is a convenience constructor for test EvaluatorOpts.
func evalCfgOpts() EvaluatorOpts {
	return EvaluatorOpts{
		MappingTarget:  "mapping-server:8443",
		RemoteWriteURL: "http://mimir:9090/api/v1/push",
		TenantLabel:    "acme",
	}
}

// unmarshalEvalCfg renders SharedEvaluatorConfig and round-trips through YAML
// into a generic map. Tests should use this to assert structure rather than
// comparing raw strings.
func unmarshalEvalCfg(t *testing.T, opts EvaluatorOpts) map[string]any {
	t.Helper()
	raw := SharedEvaluatorConfig(opts)
	var out map[string]any
	if err := yaml.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("unmarshal SharedEvaluatorConfig: %v\nraw:\n%s", err, raw)
	}
	return out
}

// scrapeConfigs extracts the scrape_configs list from the parsed document.
func scrapeConfigs(t *testing.T, doc map[string]any) []any {
	t.Helper()
	v, ok := doc["scrape_configs"]
	if !ok {
		t.Fatal("doc missing scrape_configs key")
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("scrape_configs is %T, want []any", v)
	}
	return list
}

// staticTargets extracts the static_configs targets of a scrape_config entry.
func staticTargets(t *testing.T, sc map[string]any) []string {
	t.Helper()
	staticCfgs, ok := sc["static_configs"].([]any)
	if !ok || len(staticCfgs) == 0 {
		t.Fatalf("scrape_config missing static_configs: %v", sc)
	}
	raw, _ := staticCfgs[0].(map[string]any)["targets"].([]any)
	out := make([]string, 0, len(raw))
	for _, tgt := range raw {
		out = append(out, tgt.(string))
	}
	return out
}

// Test 1: receiver mode — exactly ONE scrape_config (the mapping job; the raw
// counters arrive via remote_write, never via scrape) and ONE remote_write
// with the right URL. The mapping job must set honor_labels: true so the
// devices' instance/ifIndex/iface identity labels survive instead of being
// rewritten to the mapping server's address.
func TestSharedEvaluatorConfig_MappingJobOnly(t *testing.T) {
	opts := evalCfgOpts()
	doc := unmarshalEvalCfg(t, opts)

	scs := scrapeConfigs(t, doc)
	if len(scs) != 1 {
		t.Fatalf("want exactly 1 scrape_config (mapping only — counters arrive via remote_write), got %d", len(scs))
	}

	mapping := scs[0].(map[string]any)
	if jn, _ := mapping["job_name"].(string); jn != "promhash-mapping" {
		t.Errorf("scrape[0].job_name = %q, want promhash-mapping", jn)
	}
	if tgts := staticTargets(t, mapping); len(tgts) != 1 || tgts[0] != opts.MappingTarget {
		t.Errorf("mapping targets = %v, want [%s]", tgts, opts.MappingTarget)
	}
	if mp, _ := mapping["metrics_path"].(string); mp != DefaultMappingMetricsPath {
		t.Errorf("mapping metrics_path = %q, want %q", mp, DefaultMappingMetricsPath)
	}
	if hl, _ := mapping["honor_labels"].(bool); !hl {
		t.Errorf("mapping job must set honor_labels: true, got %v", mapping["honor_labels"])
	}

	rws, ok := doc["remote_write"].([]any)
	if !ok || len(rws) != 1 {
		t.Fatalf("remote_write: expected list of length 1, got %T %v", doc["remote_write"], doc["remote_write"])
	}
	url, _ := rws[0].(map[string]any)["url"].(string)
	if url != opts.RemoteWriteURL {
		t.Errorf("remote_write.url = %q, want %q", url, opts.RemoteWriteURL)
	}
}

// Test 1b: an explicit MappingMetricsPath overrides the default.
func TestSharedEvaluatorConfig_MappingMetricsPathOverride(t *testing.T) {
	opts := evalCfgOpts()
	opts.MappingMetricsPath = "/custom/mapping"
	doc := unmarshalEvalCfg(t, opts)
	mapping := scrapeConfigs(t, doc)[0].(map[string]any)
	if mp, _ := mapping["metrics_path"].(string); mp != "/custom/mapping" {
		t.Errorf("mapping metrics_path = %q, want /custom/mapping", mp)
	}
}

// Test 1c: an empty MappingTarget omits the mapping job entirely (the CLI
// rejects that configuration; the renderer stays total).
func TestSharedEvaluatorConfig_NoMappingTargetOmitsJob(t *testing.T) {
	opts := evalCfgOpts()
	opts.MappingTarget = ""
	doc := unmarshalEvalCfg(t, opts)
	scs := scrapeConfigs(t, doc)
	if len(scs) != 0 {
		t.Fatalf("want 0 scrape_configs when MappingTarget empty, got %d", len(scs))
	}
}

// Test 2: rule_files contains BOTH the recording rules and the alerting rules.
func TestSharedEvaluatorConfig_RuleFiles(t *testing.T) {
	doc := unmarshalEvalCfg(t, evalCfgOpts())

	v, ok := doc["rule_files"]
	if !ok {
		t.Fatal("doc missing rule_files")
	}
	files, ok := v.([]any)
	if !ok {
		t.Fatalf("rule_files is %T, want []any", v)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.(string)] = true
	}
	for _, want := range []string{RulesFileName, AlertsFileName} {
		if !got[want] {
			t.Errorf("rule_files does not contain %s: %v", want, files)
		}
	}
}

// Test 3: global.external_labels.tenant == TenantLabel.
func TestSharedEvaluatorConfig_TenantExternalLabel(t *testing.T) {
	opts := evalCfgOpts()
	doc := unmarshalEvalCfg(t, opts)

	global, ok := doc["global"].(map[string]any)
	if !ok {
		t.Fatal("doc missing global block")
	}
	extLabels, ok := global["external_labels"].(map[string]any)
	if !ok {
		t.Fatal("global.external_labels missing or wrong type")
	}
	tenant, _ := extLabels["tenant"].(string)
	if tenant != opts.TenantLabel {
		t.Errorf("global.external_labels.tenant = %q, want %q", tenant, opts.TenantLabel)
	}
}

// Test 4: the receiver applies no relabeling to remote-written samples, so the
// rendered config must contain NO relabel configuration of any kind — a
// counters-side iface relabel here would be dead config implying a join that
// cannot happen.
func TestSharedEvaluatorConfig_NoRelabelConfig(t *testing.T) {
	raw := SharedEvaluatorConfig(evalCfgOpts())
	for _, forbidden := range []string{"metric_relabel_configs", "relabel_configs", "iface"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("rendered config must not contain %q (receiver mode has no scrape-time relabel point):\n%s", forbidden, raw)
		}
	}
}

// Test 5: no counters scrape job and no per-app federation job names survive
// from the earlier architectures.
func TestSharedEvaluatorConfig_NoLegacyJobs(t *testing.T) {
	raw := SharedEvaluatorConfig(evalCfgOpts())
	for _, forbidden := range []string{"promhash-fed-", "promhash-evaluator"} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("rendered config contains forbidden legacy job reference %q:\n%s", forbidden, raw)
		}
	}
}

package enrich

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// evalCfgOpts is a convenience constructor for test EvaluatorOpts.
func evalCfgOpts(jk JoinKey) EvaluatorOpts {
	return EvaluatorOpts{
		ScrapeTarget:   "raw-counters:9116",
		MappingTarget:  "mapping-server:8443",
		RemoteWriteURL: "http://mimir:9090/api/v1/push",
		TenantLabel:    "acme",
		JoinKey:        jk,
	}
}

// unmarshalEvalCfg renders SharedEvaluatorConfig and round-trips through YAML
// into a generic map. Tests should use this to assert structure rather than
// comparing raw strings.
func unmarshalEvalCfg(t *testing.T, jk JoinKey) map[string]any {
	t.Helper()
	raw := SharedEvaluatorConfig(evalCfgOpts(jk))
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

// remoteWrites extracts the remote_write list from the parsed document.
func remoteWrites(t *testing.T, doc map[string]any) []any {
	t.Helper()
	v, ok := doc["remote_write"]
	if !ok {
		t.Fatal("doc missing remote_write key")
	}
	list, ok := v.([]any)
	if !ok {
		t.Fatalf("remote_write is %T, want []any", v)
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

// Test 1: exactly TWO scrape_configs (counters + mapping) and ONE remote_write;
// remote_write url == RemoteWriteURL; the counters job targets ScrapeTarget and
// the mapping job targets MappingTarget. Without the mapping job the
// path-health rules would join against a metric the evaluator never ingests.
func TestSharedEvaluatorConfig_ScrapeJobsAndRemoteWrite(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		doc := unmarshalEvalCfg(t, jk)
		opts := evalCfgOpts(jk)

		scs := scrapeConfigs(t, doc)
		if len(scs) != 2 {
			t.Fatalf("jk=%v: want exactly 2 scrape_configs (counters + mapping), got %d", jk, len(scs))
		}

		counters := scs[0].(map[string]any)
		if jn, _ := counters["job_name"].(string); jn != "promhash-evaluator" {
			t.Errorf("jk=%v: scrape[0].job_name = %q, want promhash-evaluator", jk, jn)
		}
		if tgts := staticTargets(t, counters); len(tgts) != 1 || tgts[0] != opts.ScrapeTarget {
			t.Errorf("jk=%v: counters targets = %v, want [%s]", jk, tgts, opts.ScrapeTarget)
		}

		mapping := scs[1].(map[string]any)
		if jn, _ := mapping["job_name"].(string); jn != "promhash-mapping" {
			t.Errorf("jk=%v: scrape[1].job_name = %q, want promhash-mapping", jk, jn)
		}
		if tgts := staticTargets(t, mapping); len(tgts) != 1 || tgts[0] != opts.MappingTarget {
			t.Errorf("jk=%v: mapping targets = %v, want [%s]", jk, tgts, opts.MappingTarget)
		}
		if mp, _ := mapping["metrics_path"].(string); mp != DefaultMappingMetricsPath {
			t.Errorf("jk=%v: mapping metrics_path = %q, want %q", jk, mp, DefaultMappingMetricsPath)
		}

		rws := remoteWrites(t, doc)
		if len(rws) != 1 {
			t.Errorf("jk=%v: want exactly 1 remote_write, got %d", jk, len(rws))
		} else {
			url, _ := rws[0].(map[string]any)["url"].(string)
			if url != opts.RemoteWriteURL {
				t.Errorf("jk=%v: remote_write.url = %q, want %q", jk, url, opts.RemoteWriteURL)
			}
		}
	}
}

// Test 1b: an explicit MappingMetricsPath overrides the default.
func TestSharedEvaluatorConfig_MappingMetricsPathOverride(t *testing.T) {
	opts := evalCfgOpts(JoinByComposite)
	opts.MappingMetricsPath = "/custom/mapping"
	raw := SharedEvaluatorConfig(opts)
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	scs := scrapeConfigs(t, doc)
	mapping := scs[1].(map[string]any)
	if mp, _ := mapping["metrics_path"].(string); mp != "/custom/mapping" {
		t.Errorf("mapping metrics_path = %q, want /custom/mapping", mp)
	}
}

// Test 1c: an empty MappingTarget omits the mapping job entirely (the CLI
// rejects that configuration; the renderer stays total).
func TestSharedEvaluatorConfig_NoMappingTargetOmitsJob(t *testing.T) {
	opts := evalCfgOpts(JoinByComposite)
	opts.MappingTarget = ""
	raw := SharedEvaluatorConfig(opts)
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	scs := scrapeConfigs(t, doc)
	if len(scs) != 1 {
		t.Fatalf("want 1 scrape_config when MappingTarget empty, got %d", len(scs))
	}
}

// Test 2: rule_files contains "path-health.rules.yaml".
func TestSharedEvaluatorConfig_RuleFiles(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		doc := unmarshalEvalCfg(t, jk)

		v, ok := doc["rule_files"]
		if !ok {
			t.Fatalf("jk=%v: doc missing rule_files", jk)
		}
		files, ok := v.([]any)
		if !ok {
			t.Fatalf("jk=%v: rule_files is %T, want []any", jk, v)
		}
		found := false
		for _, f := range files {
			if f.(string) == "path-health.rules.yaml" {
				found = true
			}
		}
		if !found {
			t.Errorf("jk=%v: rule_files does not contain path-health.rules.yaml: %v", jk, files)
		}
	}
}

// Test 3: global.external_labels.tenant == TenantLabel.
func TestSharedEvaluatorConfig_TenantExternalLabel(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		doc := unmarshalEvalCfg(t, jk)
		opts := evalCfgOpts(jk)

		global, ok := doc["global"].(map[string]any)
		if !ok {
			t.Fatalf("jk=%v: doc missing global block", jk)
		}
		extLabels, ok := global["external_labels"].(map[string]any)
		if !ok {
			t.Fatalf("jk=%v: global.external_labels missing or wrong type", jk)
		}
		tenant, _ := extLabels["tenant"].(string)
		if tenant != opts.TenantLabel {
			t.Errorf("jk=%v: global.external_labels.tenant = %q, want %q", jk, tenant, opts.TenantLabel)
		}
	}
}

// relabelForTargetLabel walks the metric_relabel_configs of the first
// scrape_config and returns the entry whose target_label == wantTarget, or nil.
func relabelForTargetLabel(t *testing.T, doc map[string]any, wantTarget string) map[string]any {
	t.Helper()
	scs := scrapeConfigs(t, doc)
	sc := scs[0].(map[string]any)
	mrc, ok := sc["metric_relabel_configs"]
	if !ok {
		return nil
	}
	entries, ok := mrc.([]any)
	if !ok {
		return nil
	}
	for _, e := range entries {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if entry["target_label"] == wantTarget {
			return entry
		}
	}
	return nil
}

// Test 4: JoinByComposite mode — the counters job's metric_relabel_configs has
// an entry with target_label=iface, separator=":", source_labels=[instance,
// ifIndex]. The mapping job must NOT carry the relabel (its exposition already
// has an explicit iface label).
func TestSharedEvaluatorConfig_CompositeMode_IfaceRelabel(t *testing.T) {
	doc := unmarshalEvalCfg(t, JoinByComposite)
	if mapping := scrapeConfigs(t, doc)[1].(map[string]any); mapping["metric_relabel_configs"] != nil {
		t.Errorf("mapping job must not carry metric_relabel_configs: %v", mapping["metric_relabel_configs"])
	}
	entry := relabelForTargetLabel(t, doc, "iface")
	if entry == nil {
		t.Fatal("JoinByComposite: no metric_relabel entry with target_label=iface")
	}

	sep, _ := entry["separator"].(string)
	if sep != ":" {
		t.Errorf("separator = %q, want \":\"", sep)
	}

	srcRaw, ok := entry["source_labels"].([]any)
	if !ok {
		t.Fatalf("source_labels is %T, want []any", entry["source_labels"])
	}
	if len(srcRaw) != 2 {
		t.Fatalf("source_labels len = %d, want 2", len(srcRaw))
	}
	if srcRaw[0].(string) != "instance" || srcRaw[1].(string) != "ifIndex" {
		t.Errorf("source_labels = %v, want [instance ifIndex]", srcRaw)
	}
}

// Test 5: JoinByIfName mode — NO iface-synthesizing metric_relabel.
func TestSharedEvaluatorConfig_IfNameMode_NoIfaceRelabel(t *testing.T) {
	doc := unmarshalEvalCfg(t, JoinByIfName)
	entry := relabelForTargetLabel(t, doc, "iface")
	if entry != nil {
		t.Errorf("JoinByIfName: unexpected metric_relabel entry with target_label=iface: %v", entry)
	}
}

// Test 6a: NO job_name containing "promhash-fed-".
func TestSharedEvaluatorConfig_NoPromhashFedJobName(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		raw := SharedEvaluatorConfig(evalCfgOpts(jk))
		if strings.Contains(raw, "promhash-fed-") {
			t.Errorf("jk=%v: rendered config contains forbidden job_name prefix promhash-fed-:\n%s", jk, raw)
		}

		// Also check via parsed doc for belt-and-suspenders
		doc := unmarshalEvalCfg(t, jk)
		for _, sc := range scrapeConfigs(t, doc) {
			jn, _ := sc.(map[string]any)["job_name"].(string)
			if strings.Contains(jn, "promhash-fed-") {
				t.Errorf("jk=%v: scrape job_name %q contains promhash-fed-", jk, jn)
			}
		}
	}
}

// Test 6b: honor_labels placement. The counters job must NOT set honor_labels
// (that was the old federation model's bug surface). The mapping job MUST set
// honor_labels: true — otherwise Prometheus rewrites the mapping's instance
// label to the mapping server's address (original moved to exported_instance),
// breaking the on(instance, ifName) join and mislabeling group_right results.
func TestSharedEvaluatorConfig_HonorLabelsOnlyOnMappingJob(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		doc := unmarshalEvalCfg(t, jk)
		scs := scrapeConfigs(t, doc)

		counters := scs[0].(map[string]any)
		if hl, ok := counters["honor_labels"]; ok && hl != false {
			t.Errorf("jk=%v: counters job must not set honor_labels, got %v", jk, hl)
		}

		mapping := scs[1].(map[string]any)
		if hl, _ := mapping["honor_labels"].(bool); !hl {
			t.Errorf("jk=%v: mapping job must set honor_labels: true, got %v", jk, mapping["honor_labels"])
		}
	}
}

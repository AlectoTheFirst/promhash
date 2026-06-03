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

// Test 1: exactly ONE scrape_config and ONE remote_write; remote_write url ==
// RemoteWriteURL; scrape target contains ScrapeTarget.
func TestSharedEvaluatorConfig_SingleScrapeAndRemoteWrite(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		doc := unmarshalEvalCfg(t, jk)
		opts := evalCfgOpts(jk)

		scs := scrapeConfigs(t, doc)
		if len(scs) != 1 {
			t.Errorf("jk=%v: want exactly 1 scrape_config, got %d", jk, len(scs))
		}

		// scrape target must contain ScrapeTarget
		sc := scs[0].(map[string]any)
		staticCfgs, ok := sc["static_configs"].([]any)
		if !ok || len(staticCfgs) == 0 {
			t.Errorf("jk=%v: scrape_config missing static_configs", jk)
		} else {
			targets, _ := staticCfgs[0].(map[string]any)["targets"].([]any)
			found := false
			for _, tgt := range targets {
				if strings.Contains(tgt.(string), opts.ScrapeTarget) {
					found = true
				}
			}
			if !found {
				t.Errorf("jk=%v: ScrapeTarget %q not found in targets %v", jk, opts.ScrapeTarget, targets)
			}
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

// Test 4: JoinByComposite mode — metric_relabel_configs has an entry with
// target_label=iface, separator=":", source_labels=[instance, ifIndex].
func TestSharedEvaluatorConfig_CompositeMode_IfaceRelabel(t *testing.T) {
	doc := unmarshalEvalCfg(t, JoinByComposite)
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

// Test 6b: "honor_labels" is absent anywhere in the rendered config.
func TestSharedEvaluatorConfig_NoHonorLabels(t *testing.T) {
	for _, jk := range []JoinKey{JoinByComposite, JoinByIfName} {
		raw := SharedEvaluatorConfig(evalCfgOpts(jk))
		if strings.Contains(raw, "honor_labels") {
			t.Errorf("jk=%v: rendered config contains forbidden honor_labels key:\n%s", jk, raw)
		}
	}
}

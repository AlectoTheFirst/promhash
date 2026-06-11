package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/AlectoTheFirst/promhash/internal/enrich"
	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// testOpts returns a populated EvaluatorOpts for use in tests.
func testOpts() enrich.EvaluatorOpts {
	return enrich.EvaluatorOpts{
		MappingTarget:  "mapping-server:8443",
		RemoteWriteURL: "http://mimir:9090/api/v1/push",
		TenantLabel:    "test-tenant",
	}
}

// allSharedArtifacts is the complete expected file set under _shared/.
var allSharedArtifacts = []string{
	"path-health.rules.yaml",
	"path-health.alerts.yaml",
	"evaluator.yaml",
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

// Test 1: writeSharedArtifacts writes all four files under tmp/_shared/.
func TestWriteSharedArtifacts_AllFilesExist(t *testing.T) {
	tmp := t.TempDir()

	if err := writeSharedArtifacts(tmp, enrich.JoinByComposite, testOpts()); err != nil {
		t.Fatalf("writeSharedArtifacts: %v", err)
	}

	for _, name := range allSharedArtifacts {
		p := filepath.Join(tmp, "_shared", name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist, got: %v", p, err)
		}
	}
}

// Test 3: path-health.rules.yaml unmarshals to a groups document with a
// group named "promhash_path_health".
func TestWriteSharedArtifacts_PathHealthRulesGroup(t *testing.T) {
	tmp := t.TempDir()

	if err := writeSharedArtifacts(tmp, enrich.JoinByComposite, testOpts()); err != nil {
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
	opts := testOpts()

	if err := writeSharedArtifacts(tmp, enrich.JoinByComposite, opts); err != nil {
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
	apps, _ := buildTwoAppInput()

	if err := writeSharedArtifacts(tmp, enrich.JoinByComposite, testOpts()); err != nil {
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
	apps, _ := buildTwoAppInput()

	// First write the shared artifacts so _shared/ exists.
	if err := writeSharedArtifacts(tmp, enrich.JoinByComposite, testOpts()); err != nil {
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

	// _shared/ must still contain all four artifacts.
	for _, name := range allSharedArtifacts {
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

// Test 8: validateRequiredFlags rejects empty/blank required flags and names
// every missing one; a fully-populated set passes.
func TestValidateRequiredFlags(t *testing.T) {
	if err := validateRequiredFlags("api:8080", "http://rw/push", "tenant-a"); err != nil {
		t.Errorf("all flags set: unexpected error: %v", err)
	}

	err := validateRequiredFlags("  ", "", "tenant-a")
	if err == nil {
		t.Fatal("expected error for missing flags, got nil")
	}
	for _, want := range []string{"-promhash-api", "-remote-write-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s; got: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "-tenant-label") {
		t.Errorf("error must not name flags that were provided; got: %v", err)
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

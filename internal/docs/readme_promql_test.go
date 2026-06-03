package docs

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestReadmePathHealthPromQL guards the README enrichment/federation section
// against stale PromQL examples. Specifically it asserts:
//
//  1. The path-health example uses group_right( (not group_left() — the counter
//     is the one side, the bounded mapping is the many side) and references
//     promhash_interface_app{ (the T1 mapping series).
//
//  2. The dashboard variable example uses iface=~"$iface" (the composite join
//     label) and does NOT contain the old cross-product form
//     instance=~"$instance", ifIndex=~"$ifIndex" that was retired in RA6/RA7.
func TestReadmePathHealthPromQL(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed: cannot determine test file path")
	}
	readmePath := filepath.Join(filepath.Dir(thisFile), "..", "..", "README.md")

	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("cannot read README at %s: %v", readmePath, err)
	}
	content := string(data)

	// --- path-health recording-rule assertions ---

	// The rules must use group_right( because the mapping series is the many side.
	// group_left( would fail with "many-to-many" on shared core links.
	if !strings.Contains(content, "group_right()") {
		t.Error("README must contain group_right() in the path-health PromQL example (not group_left)")
	}
	if strings.Contains(content, "group_left(") {
		t.Error("README must not contain group_left( in any PromQL example (the counter is the one side)")
	}

	// The rules must reference the mapping series by its exact metric name.
	if !strings.Contains(content, "promhash_interface_app{") {
		t.Error("README must reference promhash_interface_app{ in the path-health PromQL example")
	}

	// --- dashboard variable assertions ---

	// The zero-cardinality dashboard pattern must use the composite iface label.
	if !strings.Contains(content, `iface=~"$iface"`) {
		t.Error(`README must use iface=~"$iface" in the dashboard PromQL example (composite join label)`)
	}

	// The old cross-product pattern must NOT appear.
	if strings.Contains(content, `instance=~"$instance", ifIndex=~"$ifIndex"`) {
		t.Error(`README must not contain the cross-product instance=~"$instance", ifIndex=~"$ifIndex" (retired in RA6/RA7; use iface=~"$iface" instead)`)
	}
}

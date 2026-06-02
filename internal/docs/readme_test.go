package docs

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestReadmeDoesNotExposeSecretFlags guards against the README reintroducing
// secrets as CLI flags, which leak into the process list. The tools already
// read secrets from the environment (NEO4J_PASS / SERVICENOW_PASS /
// NAUTOBOT_TOKEN); the Quickstart must use those env vars, never the flags.
func TestReadmeDoesNotExposeSecretFlags(t *testing.T) {
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

	// Secret flags must NOT appear — they leak into the process list.
	forbidden := []string{
		"-neo4j-pass",
		"-servicenow-pass",
		"-nautobot-token",
	}
	for _, sub := range forbidden {
		if contains(content, sub) {
			t.Fatalf("README must not contain secret flag %q (secrets must come from env vars)", sub)
		}
	}

	// Env var names MUST appear — proving the env-var model is documented.
	required := []string{
		"NEO4J_PASS",
		"SERVICENOW_PASS",
		"NAUTOBOT_TOKEN",
	}
	for _, sub := range required {
		if !contains(content, sub) {
			t.Fatalf("README must document env var %q but it was not found", sub)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && indexStr(s, substr) >= 0)
}

func indexStr(s, substr string) int {
	n := len(substr)
	if n == 0 {
		return 0
	}
	for i := 0; i <= len(s)-n; i++ {
		if s[i:i+n] == substr {
			return i
		}
	}
	return -1
}

package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestNoFloatingActionTags guards against GitHub Actions steps that reference
// mutable major-version tags (e.g. actions/checkout@v4). These tags are
// repointed over time and represent a supply-chain risk; every action must be
// pinned to an immutable commit SHA (e.g. actions/checkout@<sha> # v4).
func TestNoFloatingActionTags(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed: cannot determine test file path")
	}

	workflowDir := filepath.Join(filepath.Dir(thisFile), "..", "..", ".github", "workflows")

	entries, err := os.ReadDir(workflowDir)
	if err != nil {
		t.Fatalf("cannot read workflow directory %s: %v", workflowDir, err)
	}

	// Matches a `uses:` line where the action ref is a bare @vN floating tag,
	// i.e. @v followed by one-or-more digits and then whitespace or end-of-line.
	// A SHA-pinned ref like @<40hexchars> # v4 does NOT match because the SHA
	// is not a bare "v<digit>" string.
	floatingTag := regexp.MustCompile(`uses:\s*\S+@v[0-9]+(\s|$)`)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(workflowDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("cannot read workflow file %s: %v", path, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if floatingTag.MatchString(line) {
				t.Errorf("%s:%d: floating action tag (pin to commit SHA): %s",
					name, i+1, strings.TrimSpace(line))
			}
		}
	}
}

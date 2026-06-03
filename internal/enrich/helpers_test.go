package enrich

import (
	"fmt"
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// TestSelectors_NumericSort verifies that ifIndexes are sorted numerically
// (not lexically) and that both instances and ifIndexes are deduplicated.
func TestSelectors_NumericSort(t *testing.T) {
	hops := []graph.Hop{
		{Instance: "10.0.0.1", IfIndex: 42},
		{Instance: "10.0.0.2", IfIndex: 9},
		{Instance: "10.0.0.1", IfIndex: 100},
		{Instance: "10.0.0.2", IfIndex: 42}, // duplicate ifIndex
	}
	insts, idxs := Selectors(hops)

	// ifIndexes must be numerically ordered and deduplicated
	wantIdxs := []string{"9", "42", "100"}
	if len(idxs) != len(wantIdxs) {
		t.Fatalf("ifIndexes: got %v, want %v", idxs, wantIdxs)
	}
	for i := range wantIdxs {
		if idxs[i] != wantIdxs[i] {
			t.Errorf("ifIndexes[%d]: got %q, want %q (full: %v)", i, idxs[i], wantIdxs[i], idxs)
		}
	}

	// instances must be deduplicated (2 unique from 4 hops)
	if len(insts) != 2 {
		t.Errorf("instances: got %v (len %d), want 2 unique", insts, len(insts))
	}
}

// TestSelectors_EmptyHops verifies that empty input returns non-nil slices
// (important for JSON marshalling: [] not null).
func TestSelectors_EmptyHops(t *testing.T) {
	insts, idxs := Selectors(nil)
	if insts == nil {
		t.Error("instances: got nil, want non-nil empty slice")
	}
	if idxs == nil {
		t.Error("ifIndexes: got nil, want non-nil empty slice")
	}
	if len(insts) != 0 {
		t.Errorf("instances: got %v, want empty", insts)
	}
	if len(idxs) != 0 {
		t.Errorf("ifIndexes: got %v, want empty", idxs)
	}
}

// TestLabelValueEscape_RoundTrip proves that labelValueEscape produces valid
// Prometheus exposition-format label values by building a real .prom snippet,
// parsing it with expfmt.TextParser, and asserting the recovered value equals
// the original. Covers backslash, double-quote, and newline.
func TestLabelValueEscape_RoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{name: "backslash_quote", value: `a"b\c`},
		{name: "newline", value: "line1\nline2"},
		{name: "combined", value: "a\"b\\c\nend"},
	}

	parser := expfmt.NewTextParser(model.LegacyValidation)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			escaped := labelValueEscape(tc.value)
			// Build a minimal valid exposition-format text block.
			prom := fmt.Sprintf("metric{l=\"%s\"} 1\n", escaped)

			mf, err := parser.TextToMetricFamilies(strings.NewReader(prom))
			if err != nil {
				t.Fatalf("parse error: %v\nprom text:\n%s", err, prom)
			}
			family, ok := mf["metric"]
			if !ok {
				t.Fatalf("metric family 'metric' not found in parsed output")
			}
			if len(family.Metric) == 0 {
				t.Fatal("no metrics parsed")
			}
			labels := family.Metric[0].Label
			if len(labels) == 0 {
				t.Fatal("no labels on parsed metric")
			}
			got := labels[0].GetValue()
			if got != tc.value {
				t.Errorf("round-trip failed:\n got: %q\nwant: %q\nescaped form: %q", got, tc.value, escaped)
			}
		})
	}
}

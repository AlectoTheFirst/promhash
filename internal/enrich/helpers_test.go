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

// TestIfaceSelectors verifies that IfaceSelectors returns exactly the real
// (instance, ifIndex) pairs present in the hops, with no cross-product.
func TestIfaceSelectors(t *testing.T) {
	t.Run("real_pairs_no_cross_product", func(t *testing.T) {
		hops := []graph.Hop{
			{Instance: "10.0.0.1", IfIndex: 42},
			{Instance: "10.0.0.2", IfIndex: 43},
		}
		got := IfaceSelectors(hops)

		want := []string{"10.0.0.1:42", "10.0.0.2:43"}
		if len(got) != len(want) {
			t.Fatalf("len: got %d (%v), want %d (%v)", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("[%d]: got %q, want %q (full: %v)", i, got[i], want[i], got)
			}
		}

		// Explicitly assert cross-product entries are absent.
		crossProduct := []string{"10.0.0.1:43", "10.0.0.2:42"}
		gotSet := make(map[string]struct{}, len(got))
		for _, s := range got {
			gotSet[s] = struct{}{}
		}
		for _, cp := range crossProduct {
			if _, ok := gotSet[cp]; ok {
				t.Errorf("cross-product entry %q must not be present", cp)
			}
		}
	})

	t.Run("dedup_duplicate_hops", func(t *testing.T) {
		hops := []graph.Hop{
			{Instance: "10.0.0.1", IfIndex: 42},
			{Instance: "10.0.0.1", IfIndex: 42}, // exact duplicate
			{Instance: "10.0.0.2", IfIndex: 43},
		}
		got := IfaceSelectors(hops)
		if len(got) != 2 {
			t.Errorf("duplicates should collapse: got %v (len %d), want len 2", got, len(got))
		}
	})

	t.Run("empty_hops_non_nil", func(t *testing.T) {
		got := IfaceSelectors(nil)
		if got == nil {
			t.Error("got nil, want non-nil empty slice")
		}
		if len(got) != 0 {
			t.Errorf("got %v, want empty slice", got)
		}
	})

	t.Run("numeric_ifindex_order_within_instance", func(t *testing.T) {
		// ifIndexes 100, 9, 42 must sort numerically (9 < 42 < 100),
		// not lexically ("100" < "42" < "9").
		hops := []graph.Hop{
			{Instance: "10.0.0.1", IfIndex: 100},
			{Instance: "10.0.0.1", IfIndex: 9},
			{Instance: "10.0.0.1", IfIndex: 42},
		}
		got := IfaceSelectors(hops)

		want := []string{"10.0.0.1:9", "10.0.0.1:42", "10.0.0.1:100"}
		if len(got) != len(want) {
			t.Fatalf("len: got %d (%v), want %d (%v)", len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("[%d]: got %q, want %q (full: %v)", i, got[i], want[i], got)
			}
		}
	})
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

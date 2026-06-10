package enrich

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// IfaceSelectors returns the deduplicated, deterministically-sorted set of
// composite "instance:ifIndex" strings for the REAL (instance, ifIndex) pairs
// present in hops. Unlike Selectors, which returns separate flat lists that a
// naive caller could cross-product, IfaceSelectors returns only the pairs that
// actually exist — so a dashboard variable built from this slice has exactly
// the right members with no cross-product inflation.
//
// The slice is non-nil even when hops is empty, so it marshals to a JSON array
// rather than null.
func IfaceSelectors(hops []graph.Hop) []string {
	type pair struct {
		instance string
		ifIndex  int
	}
	seen := map[pair]struct{}{}
	for _, h := range hops {
		seen[pair{h.Instance, h.IfIndex}] = struct{}{}
	}
	pairs := make([]pair, 0, len(seen))
	for p := range seen {
		pairs = append(pairs, p)
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if c := cmp.Compare(a.instance, b.instance); c != 0 {
			return c
		}
		return cmp.Compare(a.ifIndex, b.ifIndex)
	})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.instance+":"+strconv.Itoa(p.ifIndex))
	}
	return out
}

// labelValueEscape escapes a string for use as a Prometheus exposition-format
// label value. It applies the three substitutions required by the text format
// specification:
//
//	\  →  \\
//	"  →  \"
//	newline  →  \n
//
// This is NOT YAML escaping; it is used when constructing the raw .prom text
// that the exposition-format parser will consume.
func labelValueEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

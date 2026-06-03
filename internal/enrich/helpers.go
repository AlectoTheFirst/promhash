package enrich

import (
	"cmp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// Selectors extracts the unique instance addresses and ifIndex values from a
// set of hops. Instances are returned as raw strings (callers are responsible
// for any regex escaping). ifIndexes are deduplicated and sorted numerically
// (so [42, 9, 100] → ["9","42","100"], never lexical ["100","42","9"]).
// Both slices are non-nil even when hops is empty, so they marshal to JSON
// arrays rather than null.
func Selectors(hops []graph.Hop) (instances []string, ifIndexes []string) {
	instSet := map[string]struct{}{}
	idxSet := map[int]struct{}{}
	for _, h := range hops {
		instSet[h.Instance] = struct{}{}
		idxSet[h.IfIndex] = struct{}{}
	}

	instances = make([]string, 0, len(instSet))
	for k := range instSet {
		instances = append(instances, k)
	}
	sort.Strings(instances)

	idxInts := make([]int, 0, len(idxSet))
	for k := range idxSet {
		idxInts = append(idxInts, k)
	}
	sort.Ints(idxInts)
	ifIndexes = make([]string, 0, len(idxInts))
	for _, v := range idxInts {
		ifIndexes = append(ifIndexes, strconv.Itoa(v))
	}

	return instances, ifIndexes
}

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

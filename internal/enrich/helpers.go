package enrich

import (
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

// Package catalog builds and queries the canonical interface catalog: it
// normalizes vendor-specific interface names, synchronizes harvested Prometheus
// interface rows into graph Interface nodes, and resolves human-supplied
// interface references to exactly one catalog interface.
package catalog

import (
	"regexp"

	"github.com/AlectoTheFirst/promhash/internal/phash"
)

// abbrev maps short vendor forms to their canonical long form. Matching is an
// exact equality check on the entire leading alpha token (the full [a-z]+ run);
// the first matching entry wins.
var abbrev = []struct{ short, long string }{
	{"te", "tengige"},
	{"gi", "gigabitethernet"},
	{"eth", "ethernet"},
	{"et", "ethernet"},
	{"fa", "fastethernet"},
	{"hu", "hundredgige"},
}

var prefixRe = regexp.MustCompile(`^([a-z]+)(.*)$`)

// CanonicalIfName normalizes and expands vendor abbreviations on the leading
// alpha token of an interface name. Normalization applies phash.NormalizeToken
// (NFKC + Unicode-whitespace collapse + lowercase) so that full-width variants,
// NBSP-separated tokens, and mixed-case inputs all produce the same canonical
// output. Juniper-style names (with '-') are returned after normalization
// without abbreviation expansion.
func CanonicalIfName(vendor, raw string) string {
	s := phash.NormalizeToken(raw)
	if s == "" {
		return s
	}
	if containsDash(s) {
		return s
	} // juniper ge-0/0/3, xe-0/1/2
	m := prefixRe.FindStringSubmatch(s)
	if m == nil {
		return s
	}
	head, tail := m[1], m[2]
	best := head
	for _, a := range abbrev {
		if head == a.short {
			best = a.long
			break
		}
	}
	return best + tail
}

// containsDash reports whether s contains a '-' character. Extracted to avoid
// importing "strings" solely for one Contains call now that the strings import
// was replaced by the phash import.
func containsDash(s string) bool {
	for _, c := range s {
		if c == '-' {
			return true
		}
	}
	return false
}

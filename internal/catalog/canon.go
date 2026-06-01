// Package catalog builds and queries the canonical interface catalog: it
// normalizes vendor-specific interface names, synchronizes harvested Prometheus
// interface rows into graph Interface nodes, and resolves human-supplied
// interface references to exactly one catalog interface.
package catalog

import (
	"regexp"
	"strings"
)

// abbrev maps short vendor forms to long; applied longest-prefix-first.
var abbrev = []struct{ short, long string }{
	{"tengige", "tengige"}, {"te", "tengige"},
	{"gigabitethernet", "gigabitethernet"}, {"gi", "gigabitethernet"},
	{"ethernet", "ethernet"}, {"eth", "ethernet"},
	{"fastethernet", "fastethernet"}, {"fa", "fastethernet"},
	{"hundredgige", "hundredgige"}, {"hu", "hundredgige"},
}

var prefixRe = regexp.MustCompile(`^([a-z]+)(.*)$`)

// CanonicalIfName lowercases, trims, and expands vendor abbreviations on the
// leading alpha token. Juniper-style names (with '-') are left as-is after norm.
func CanonicalIfName(vendor, raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(s, "-") {
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

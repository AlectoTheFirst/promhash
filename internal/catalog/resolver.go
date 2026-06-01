package catalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/starkweb/promhash/internal/graph"
)

// NoMatchError reports that no interface on the device matched the requested
// reference. Suggestions holds a short, sorted list of available interface
// names to help the caller correct the reference.
type NoMatchError struct {
	Device, Ref string
	Suggestions []string
}

// Error implements the error interface, naming the device, the unmatched
// reference, and the suggested interface names.
func (e *NoMatchError) Error() string {
	return fmt.Sprintf("no interface on %q matches %q; did you mean: %s",
		e.Device, e.Ref, strings.Join(e.Suggestions, ", "))
}

// AmbiguousError reports that a reference matched more than one interface on the
// device. Matches holds the metric ifName of every interface that matched.
type AmbiguousError struct {
	Device, Ref string
	Matches     []string
}

// Error implements the error interface, naming the device, the ambiguous
// reference, and every interface it matched.
func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("ref %q on %q is ambiguous: %s", e.Ref, e.Device, strings.Join(e.Matches, ", "))
}

// Resolver matches human-supplied interface references to catalog interfaces,
// indexing the known interfaces by device for lookup.
type Resolver struct{ byDevice map[string][]graph.Iface }

// NewResolver builds a Resolver from the given interfaces, grouping them by
// their Device for subsequent Resolve calls.
func NewResolver(ifaces []graph.Iface) *Resolver {
	m := map[string][]graph.Iface{}
	for _, i := range ifaces {
		m[i.Device] = append(m[i.Device], i)
	}
	return &Resolver{byDevice: m}
}

// Resolve maps (device, human ref) to exactly one catalog interface, matching
// against canonical ifName, ifDescr, or ifAlias. Zero/many matches fail loud.
func (r *Resolver) Resolve(device, ref string) (graph.Iface, error) {
	list := r.byDevice[device]
	want := CanonicalIfName("", ref)
	refLower := strings.ToLower(strings.TrimSpace(ref))
	var hits []graph.Iface
	for _, i := range list {
		switch {
		case i.IfName == want,
			CanonicalIfName(i.Vendor, i.IfDescr) == want,
			strings.ToLower(i.IfAlias) == refLower,
			strings.ToLower(i.MetricIfName) == refLower:
			hits = append(hits, i)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return graph.Iface{}, &NoMatchError{Device: device, Ref: ref, Suggestions: suggest(list)}
	default:
		names := make([]string, len(hits))
		for i, h := range hits {
			names[i] = h.MetricIfName
		}
		return graph.Iface{}, &AmbiguousError{Device: device, Ref: ref, Matches: names}
	}
}

func suggest(list []graph.Iface) []string {
	out := make([]string, 0, len(list))
	for _, i := range list {
		out = append(out, i.MetricIfName)
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

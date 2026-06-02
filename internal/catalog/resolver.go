package catalog

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

// NoMatchError reports that no interface on the device matched the requested
// reference. Suggestions holds a short, sorted list of available interface
// names to help the caller correct the reference.
type NoMatchError struct {
	Device, Ref string
	Suggestions []string
}

// Error implements the error interface, naming the device and the unmatched
// reference. When suggestions are available (known device with interfaces) it
// appends a "did you mean" hint; when there are no suggestions it reports that
// no interfaces are known for the device.
func (e *NoMatchError) Error() string {
	if len(e.Suggestions) == 0 {
		return fmt.Sprintf("no interface on %q matches %q (no interfaces known for this device)",
			e.Device, e.Ref)
	}
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
// their normalized Device for subsequent Resolve calls. Normalization uses
// phash.NormDevice (lower+trim), matching what Sync stores and what Hash
// applies, so look-ups are case/whitespace-insensitive.
func NewResolver(ifaces []graph.Iface) *Resolver {
	m := map[string][]graph.Iface{}
	for _, i := range ifaces {
		key := phash.NormDevice(i.Device)
		m[key] = append(m[key], i)
	}
	return &Resolver{byDevice: m}
}

// Resolve maps (device, human ref) to exactly one catalog interface, matching
// against canonical ifName, ifDescr, or ifAlias. Zero/many matches fail loud.
// The device argument is normalized before lookup so callers may pass any
// case/whitespace variant of the device name.
func (r *Resolver) Resolve(device, ref string) (graph.Iface, error) {
	list := r.byDevice[phash.NormDevice(device)]
	if strings.TrimSpace(ref) == "" {
		return graph.Iface{}, &NoMatchError{Device: device, Ref: ref, Suggestions: suggest(list)}
	}
	want := CanonicalIfName("", ref)
	refLower := strings.ToLower(strings.TrimSpace(ref))
	var hits []graph.Iface
	for _, i := range list {
		switch {
		case want != "" && i.IfName == want,
			want != "" && CanonicalIfName(i.Vendor, i.IfDescr) == want,
			refLower != "" && strings.ToLower(i.IfAlias) == refLower,
			refLower != "" && strings.ToLower(i.MetricIfName) == refLower:
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

package enrich

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// JoinKey selects which label the downstream recording-rule group_left will
// use to join the mapping series against raw counters. Both IfName and Iface
// are always populated in every MappingPoint regardless of the JoinKey, so
// this parameter is currently inert within RA1; it is threaded so that RA2
// and RA4 can read it from a shared config without requiring a new type.
type JoinKey int

const (
	// JoinByIfName uses the ifName label as the join column.
	JoinByIfName JoinKey = iota
	// JoinByComposite uses the composite iface label (instance:ifIndex) as the
	// join column.
	JoinByComposite
)

// MappingPoint is one row of the promhash_interface_app{…}=1 mapping series.
// Every field maps directly to a Prometheus label of the same name (in camelCase
// for multi-word labels; see RenderMappingSeries for the exact label names used
// in the exposition text).
type MappingPoint struct {
	Instance  string
	Device    string
	IfName    string // MetricIfName from the hop
	Iface     string // synthesized as "instance:ifIndex"
	App       string
	Service   string
	Direction string
	IfIndex   int
}

// dedupKey is the composite identity used to collapse duplicate MappingPoints.
// The same physical interface traversed by the same app in the same direction
// should appear exactly once, regardless of how many candidate paths include it.
type dedupKey struct {
	instance  string
	ifIndex   int
	app       string
	direction string
}

// MappingSeries builds the set of MappingPoints for a single (app, service)
// pair over the given hops.
//
// Transit expansion: a hop whose Direction is "transit" expands into TWO
// MappingPoints — one with Direction="ingress" and one with Direction="egress"
// — because a transit interface carries traffic in both directions.
//
// Deduplication: points are deduplicated by the composite key
// (instance, ifIndex, app, direction). The same physical interface appearing
// in multiple candidate paths (at different Seq values) collapses to at most
// one point per direction.
//
// The returned slice is deterministically sorted by (direction, instance,
// ifIndex, app, service) and is non-nil even when empty.
//
// jk is threaded for downstream use by RA2/RA4; it does not alter point
// contents in RA1 since both IfName and Iface are always populated.
func MappingSeries(app, service string, hops []graph.Hop, jk JoinKey) []MappingPoint {
	_ = jk // inert in RA1; see JoinKey doc

	seen := map[dedupKey]struct{}{}
	pts := make([]MappingPoint, 0, len(hops))

	emit := func(h graph.Hop, dir string) {
		k := dedupKey{instance: h.Instance, ifIndex: h.IfIndex, app: app, direction: dir}
		if _, dup := seen[k]; dup {
			return
		}
		seen[k] = struct{}{}
		pts = append(pts, MappingPoint{
			Instance:  h.Instance,
			Device:    h.Device,
			IfName:    h.MetricIfName,
			Iface:     h.Instance + ":" + strconv.Itoa(h.IfIndex),
			App:       app,
			Service:   service,
			Direction: dir,
			IfIndex:   h.IfIndex,
		})
	}

	for _, h := range hops {
		if h.Direction == "transit" {
			emit(h, "ingress")
			emit(h, "egress")
		} else {
			emit(h, h.Direction)
		}
	}

	sortPoints(pts)
	return pts
}

// sortPoints sorts a slice of MappingPoints in place by
// (direction, instance, ifIndex, app, service). The comparator is a total
// order over all distinct MappingPoint values, so sort.Slice (which is not
// stable) still produces a deterministic result even when slices from
// multiple apps are concatenated before sorting.
func sortPoints(pts []MappingPoint) {
	sort.Slice(pts, func(i, j int) bool {
		a, b := pts[i], pts[j]
		if a.Direction != b.Direction {
			return a.Direction < b.Direction
		}
		if a.Instance != b.Instance {
			return a.Instance < b.Instance
		}
		if a.IfIndex != b.IfIndex {
			return a.IfIndex < b.IfIndex
		}
		if a.App != b.App {
			return a.App < b.App
		}
		return a.Service < b.Service
	})
}

// RenderMappingSeries emits Prometheus exposition text for the given points.
// Each point produces one line of the form:
//
//	promhash_interface_app{app="…",service="…",device="…",ifName="…",instance="…",ifIndex="N",iface="inst:idx",direction="…"} 1
//
// Label values are escaped using labelValueEscape. Lines end with \n. The
// output is deterministic regardless of the order in which points are
// supplied: RenderMappingSeries sorts a local copy by the total order
// (direction, instance, ifIndex, app, service) before emitting.
func RenderMappingSeries(points []MappingPoint) string {
	sorted := make([]MappingPoint, len(points))
	copy(sorted, points)
	sortPoints(sorted)

	var b strings.Builder
	for _, p := range sorted {
		fmt.Fprintf(&b, `promhash_interface_app{app="%s",service="%s",device="%s",ifName="%s",instance="%s",ifIndex="%d",iface="%s",direction="%s"} 1`+"\n",
			labelValueEscape(p.App),
			labelValueEscape(p.Service),
			labelValueEscape(p.Device),
			labelValueEscape(p.IfName),
			labelValueEscape(p.Instance),
			p.IfIndex,
			labelValueEscape(p.Iface),
			labelValueEscape(p.Direction),
		)
	}
	return b.String()
}

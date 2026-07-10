package catalog

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

// ifacePHash is the canonical interface identity (device + canonical ifName).
func ifacePHash(device, canonicalIfName string) string {
	return phash.Hash(phash.KindIface, device, canonicalIfName)
}

// builtCatalog is the pure outcome of converting harvested rows into Interface
// nodes: the rows to persist, how many were skipped, and human-readable
// warnings (skips and identity collisions) for the caller to log.
type builtCatalog struct {
	ifaces   []graph.Iface
	skipped  int
	warnings []string
}

// buildIfaces converts harvested Prometheus rows into Interface nodes without
// touching the graph, so the conversion rules are unit-testable.
//
// Device-name precedence per row: the harvested device label wins; then the
// optional Nautobot instance→device map; then the HOST PART of the raw
// instance as a last resort (a Prometheus instance is almost always
// "host:port", and the raw value would always be rejected by SafeKey for
// containing ':').
//
// Rows whose normalized device or canonical ifName still contain ':' or
// control characters, or that yield an empty canonical name, are skipped with
// a warning. Two rows collapsing to the same identity phash from different
// instances (e.g. two exporters scraping one device name) produce an
// identity-collision warning; the later row wins, matching MERGE semantics.
func buildIfaces(rows []promclient.IfaceRow, devByInstance map[string]string, vendor string, now time.Time) builtCatalog {
	var b builtCatalog
	firstInstance := map[string]string{} // phash -> first instance seen
	for _, row := range rows {
		raw := row.Device
		if raw == "" {
			raw = devByInstance[row.Instance]
		}
		if raw == "" {
			raw = row.Instance
			if host, _, err := net.SplitHostPort(raw); err == nil {
				raw = host
			}
		}
		dev := phash.NormDevice(raw)
		canon := CanonicalIfName(vendor, row.IfName)
		if canon == "" {
			canon = CanonicalIfName(vendor, row.IfDescr)
		}
		if canon == "" {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"skipping row with empty canonical name (device=%q ifName=%q ifDescr=%q)",
				dev, row.IfName, row.IfDescr))
			b.skipped++
			continue
		}
		if err := phash.SafeKey("device", dev); err != nil {
			b.warnings = append(b.warnings, fmt.Sprintf("skipping row (instance=%q): %v", row.Instance, err))
			b.skipped++
			continue
		}
		if err := phash.SafeKey("ifName", canon); err != nil {
			b.warnings = append(b.warnings, fmt.Sprintf("skipping row (instance=%q ifName=%q): %v", row.Instance, canon, err))
			b.skipped++
			continue
		}
		ph := ifacePHash(dev, canon)
		if prev, seen := firstInstance[ph]; seen && prev != row.Instance {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"identity collision: (device=%q ifName=%q) harvested from instances %q and %q; last write wins",
				dev, canon, prev, row.Instance))
		} else if !seen {
			firstInstance[ph] = row.Instance
		}
		b.ifaces = append(b.ifaces, graph.Iface{
			PHash: ph, Device: dev, IfName: canon,
			MetricIfName: row.IfName, IfDescr: row.IfDescr, IfAlias: row.IfAlias,
			Instance: row.Instance, Vendor: vendor, IfIndex: row.IfIndex, ObservedAt: now})
	}
	return b
}

// Sync turns harvested Prometheus rows into Interface nodes, keyed by canonical
// (device, ifName), binding the real metric labels and current ifIndex. Rows
// are converted by buildIfaces (see its doc for precedence and skip rules) and
// written in batches via UpsertInterfaces.
func Sync(ctx context.Context, r *graph.Repo, rows []promclient.IfaceRow,
	devByInstance map[string]string, vendor string) error {
	b := buildIfaces(rows, devByInstance, vendor, time.Now().UTC())
	for _, w := range b.warnings {
		log.Printf("catalog.Sync: %s", w)
	}
	if err := r.UpsertInterfaces(ctx, b.ifaces); err != nil {
		return err
	}
	if b.skipped > 0 {
		log.Printf("catalog.Sync: skipped %d row(s) with invalid device/ifName", b.skipped)
	}
	return nil
}

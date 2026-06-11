package catalog

import (
	"context"
	"log"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

// ifacePHash is the canonical interface identity (device + canonical ifName).
func ifacePHash(device, canonicalIfName string) string {
	return phash.Hash(phash.KindIface, device, canonicalIfName)
}

// Sync turns harvested Prometheus rows into Interface nodes, keyed by canonical
// (device, ifName), binding the real metric labels and current ifIndex.
// Rows whose normalized device or canonical ifName contain ':' or control
// characters are skipped with a warning rather than aborting the batch.
//
// Device-name precedence per row: the harvested device label (row.Device,
// e.g. a hostname label stamped by file_sd target files) wins; then the
// optional Nautobot instance→device map; then the raw instance as a last
// resort.
func Sync(ctx context.Context, r *graph.Repo, rows []promclient.IfaceRow,
	devByInstance map[string]string, vendor string) error {
	now := time.Now().UTC()
	var skipped int
	for _, row := range rows {
		raw := row.Device
		if raw == "" {
			raw = devByInstance[row.Instance]
		}
		if raw == "" {
			raw = row.Instance
		}
		dev := phash.NormDevice(raw)
		canon := CanonicalIfName(vendor, row.IfName)
		if canon == "" {
			canon = CanonicalIfName(vendor, row.IfDescr)
		}
		if canon == "" {
			log.Printf("catalog.Sync: skipping row with empty canonical name (device=%q ifName=%q ifDescr=%q)",
				dev, row.IfName, row.IfDescr)
			skipped++
			continue
		}
		if err := phash.SafeKey("device", dev); err != nil {
			log.Printf("catalog.Sync: skipping row (instance=%q): %v", row.Instance, err)
			skipped++
			continue
		}
		if err := phash.SafeKey("ifName", canon); err != nil {
			log.Printf("catalog.Sync: skipping row (instance=%q ifName=%q): %v", row.Instance, canon, err)
			skipped++
			continue
		}
		ifc := graph.Iface{
			PHash: ifacePHash(dev, canon), Device: dev, IfName: canon,
			MetricIfName: row.IfName, IfDescr: row.IfDescr, IfAlias: row.IfAlias,
			Instance: row.Instance, Vendor: vendor, IfIndex: row.IfIndex, ObservedAt: now}
		if err := r.UpsertInterface(ctx, ifc); err != nil {
			return err
		}
	}
	if skipped > 0 {
		log.Printf("catalog.Sync: skipped %d row(s) with invalid device/ifName", skipped)
	}
	return nil
}

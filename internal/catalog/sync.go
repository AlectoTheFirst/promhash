package catalog

import (
	"context"
	"time"

	"github.com/starkweb/promhash/internal/graph"
	"github.com/starkweb/promhash/internal/phash"
	"github.com/starkweb/promhash/internal/promclient"
)

// ifacePHash is the canonical interface identity (device + canonical ifName).
func ifacePHash(device, canonicalIfName string) string {
	return phash.Hash(phash.KindIface, device, canonicalIfName)
}

// Sync turns harvested Prometheus rows into Interface nodes, keyed by canonical
// (device, ifName), binding the real metric labels and current ifIndex.
func Sync(ctx context.Context, r *graph.Repo, rows []promclient.IfaceRow,
	devByInstance map[string]string, vendor string) error {
	now := time.Now().UTC()
	for _, row := range rows {
		device := devByInstance[row.Instance]
		if device == "" {
			device = row.Instance
		} // fall back to instance if unmapped
		canon := CanonicalIfName(vendor, row.IfName)
		if canon == "" {
			canon = CanonicalIfName(vendor, row.IfDescr)
		}
		ifc := graph.Iface{
			PHash: ifacePHash(device, canon), Device: device, IfName: canon,
			MetricIfName: row.IfName, IfDescr: row.IfDescr, IfAlias: row.IfAlias,
			Instance: row.Instance, Vendor: vendor, IfIndex: row.IfIndex, ObservedAt: now}
		if err := r.UpsertInterface(ctx, ifc); err != nil {
			return err
		}
	}
	return nil
}

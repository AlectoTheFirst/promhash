package catalog

import (
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// OldestObservedAt returns the minimum non-zero ObservedAt across ifaces.
// Zero-value ObservedAt entries are ignored. If no non-zero entry exists,
// the zero time is returned.
func OldestObservedAt(ifaces []graph.Iface) time.Time {
	var oldest time.Time
	for _, ifc := range ifaces {
		if ifc.ObservedAt.IsZero() {
			continue
		}
		if oldest.IsZero() || ifc.ObservedAt.Before(oldest) {
			oldest = ifc.ObservedAt
		}
	}
	return oldest
}

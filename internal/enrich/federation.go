// Package enrich generates Prometheus federation artifacts for a single app's
// traffic path. From the set of network hops a flow traverses, it builds the
// federation match selector, the per-app federation scrape config, and the
// recording-rule group that materializes per-hop interface octet rates into a
// curated tenant Prometheus.
package enrich

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// FederationMatch builds a Prometheus /federate match[] selector that captures
// the interface octet and operational-status series for every hop in hops. The
// instance and ifIndex label values are deduplicated and sorted, then joined
// into regex alternations so the selector is deterministic regardless of hop
// order.
func FederationMatch(hops []graph.Hop) string {
	insts, idxs := Selectors(hops)
	// instance values may contain regex metacharacters (e.g. ':' in host:port
	// is benign, but '.' '[' etc. are not), so escape them before joining into
	// the instance=~ alternation. ifIndex is an integer and needs no escaping.
	escaped := make([]string, len(insts))
	for i, inst := range insts {
		escaped[i] = regexp.QuoteMeta(inst)
	}
	return fmt.Sprintf(`{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"%s", ifIndex=~"%s"}`,
		strings.Join(escaped, "|"), strings.Join(idxs, "|"))
}

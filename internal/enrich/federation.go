// Package enrich generates Prometheus federation artifacts for a single app's
// traffic path. From the set of network hops a flow traverses, it builds the
// federation match selector, the per-app federation scrape config, and the
// recording-rule group that materializes per-hop interface octet rates into a
// curated tenant Prometheus.
package enrich

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/starkweb/promhash/internal/graph"
)

// FederationMatch builds a Prometheus /federate match[] selector that captures
// the interface octet and operational-status series for every hop in hops. The
// instance and ifIndex label values are deduplicated and sorted, then joined
// into regex alternations so the selector is deterministic regardless of hop
// order.
func FederationMatch(hops []graph.Hop) string {
	insts, idxs := map[string]struct{}{}, map[string]struct{}{}
	for _, h := range hops {
		insts[h.Instance] = struct{}{}
		idxs[strconv.Itoa(h.IfIndex)] = struct{}{}
	}
	return fmt.Sprintf(`{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"%s", ifIndex=~"%s"}`,
		strings.Join(sortedKeys(insts), "|"), strings.Join(sortedKeys(idxs), "|"))
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

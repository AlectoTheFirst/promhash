package enrich

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/starkweb/promhash/internal/graph"
)

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

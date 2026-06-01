package enrich

import (
	"fmt"
	"strings"

	"github.com/starkweb/promhash/internal/graph"
)

// RuleGroup emits one recording rule per hop (no cross-candidate-path summation),
// choosing ifHCIn/OutOctets by hop direction. coverage=declared stamps provenance.
func RuleGroup(app, service string, hops []graph.Hop) string {
	var b strings.Builder
	fmt.Fprintf(&b, "groups:\n- name: promhash_%s\n  rules:\n", app)
	job := "promhash-fed-" + app
	for _, h := range hops {
		metric, dir := "ifHCInOctets", "ingress"
		if h.Direction == "egress" {
			metric, dir = "ifHCOutOctets", "egress"
		}
		fmt.Fprintf(&b,
			"  - record: app:if_%s_octets:rate5m\n    expr: rate(%s{job=\"%s\", instance=\"%s\", ifIndex=\"%d\"}[5m])\n    labels: {app: %s, service: %s, device: %s, ifName: %s, coverage: declared}\n",
			dir, metric, job, h.Instance, h.IfIndex, app, service, h.Device, h.MetricIfName)
	}
	return b.String()
}

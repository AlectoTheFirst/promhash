package enrich

import (
	"fmt"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// RuleGroup renders a Prometheus recording-rule group (as YAML) that emits one
// or more rules per hop, with no cross-candidate-path summation. Each rule
// records a 5m octet rate, selecting the metric from the hop's direction:
// egress -> ifHCOutOctets, ingress -> ifHCInOctets, and transit -> BOTH an
// ingress (ifHCInOctets) and an egress (ifHCOutOctets) rule for that hop. Every
// rule stamps app/service/device/ifName plus coverage=declared as provenance
// labels.
func RuleGroup(app, service string, hops []graph.Hop) string {
	var b strings.Builder
	fmt.Fprintf(&b, "groups:\n- name: promhash_%s\n  rules:\n", app)
	job := "promhash-fed-" + app
	for _, h := range hops {
		switch h.Direction {
		case "egress":
			writeRule(&b, "egress", "ifHCOutOctets", job, app, service, h)
		case "transit":
			writeRule(&b, "ingress", "ifHCInOctets", job, app, service, h)
			writeRule(&b, "egress", "ifHCOutOctets", job, app, service, h)
		default: // ingress
			writeRule(&b, "ingress", "ifHCInOctets", job, app, service, h)
		}
	}
	return b.String()
}

// writeRule appends a single recording rule for one hop in one direction.
func writeRule(b *strings.Builder, dir, metric, job, app, service string, h graph.Hop) {
	fmt.Fprintf(b,
		"  - record: app:if_%s_octets:rate5m\n    expr: rate(%s{job=\"%s\", instance=\"%s\", ifIndex=\"%d\"}[5m])\n    labels: {app: %s, service: %s, device: %s, ifName: %s, coverage: declared}\n",
		dir, metric, job, h.Instance, h.IfIndex, app, service, h.Device, h.MetricIfName)
}

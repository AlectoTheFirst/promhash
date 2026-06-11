package enrich

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// pathHealthGroupName is the name of the single static recording-rule group
// emitted by PathHealthRules.
const pathHealthGroupName = "promhash_path_health"

// pathHealthRule is one (record, expr) pair in the path-health rule group.
type pathHealthRule struct {
	Record string `yaml:"record"`
	Expr   string `yaml:"expr"`
}

// joinKeyLabels returns the comma-separated label list that identifies a
// physical interface for the given JoinKey. It is used both inside the on(...)
// join clause and as the by(...) grouping when pre-collapsing ALERTS.
func joinKeyLabels(jk JoinKey) string {
	switch jk {
	case JoinByIfName:
		return "instance, ifName"
	default: // JoinByComposite
		return "iface"
	}
}

// joinKeys renders the on(...) join-key clause used by the group_right() rules
// for the given JoinKey. JoinByComposite joins on the synthesized composite iface
// label; JoinByIfName joins on (instance, ifName).
func joinKeys(jk JoinKey) string {
	return "on(" + joinKeyLabels(jk) + ")"
}

// pathHealthRules builds the ordered slice of recording rules for the path-health
// group. The rules are app-independent: they join the raw SNMP counters
// against the bounded promhash_interface_app{…}=1 mapping series via group_right,
// fanning app/service/device/ifName (and the rest of the mapping's identity
// labels) onto the counters without ever stamping an app label on the raw series
// themselves.
//
// The join clause (on(...)) is derived from jk via joinKeys. The counter is the
// LEFT ("one") operand and the bounded mapping series is the RIGHT ("many")
// operand under group_right(): a single physical interface can be mapped to N
// apps, so the mapping must be the many side or the join would reject the
// duplicate-on-one-side match group. The result therefore inherits the mapping's
// full label set, and the empty group_right() clause keeps the join-key labels
// out of the group clause (so JoinByIfName, which joins on(instance, ifName),
// does not put ifName in both ON and GROUP).
//
// Two different right-operand shapes are used deliberately:
//
//   - the RAW mapping (promhash_interface_app, direction retained) for the
//     octet rates (filtered per direction) and for capacity, so capacity exists
//     per mapped direction and the utilization division can match one-to-one
//     on the full label set including direction;
//   - the COLLAPSED mapping (max without(direction)(…)) for direction-agnostic
//     facts (oper-up, errors, discards, firing alerts), so a transit interface
//     — which emits BOTH an ingress and an egress mapping series — yields
//     exactly one result series per (interface, app) and is never
//     double-counted by the rollups.
//
// This slice is the single source of truth for both PathHealthRules' YAML
// rendering and any test that wishes to recover the exact exprs.
func pathHealthRules(jk JoinKey) []pathHealthRule {
	keys := joinKeys(jk)
	gr := "group_right()"
	// collapsed is the direction-agnostic mapping: exactly one series per
	// physical (interface, app) regardless of transit's two-direction expansion.
	collapsed := "max without(direction)(promhash_interface_app)"

	return []pathHealthRule{
		// Per-hop egress octet rate, with the mapping identity fanned on, for
		// direction=egress mapping rows only.
		{
			Record: "app:if_egress_octets:rate5m",
			Expr:   "rate(ifHCOutOctets[5m]) * " + keys + " " + gr + ` promhash_interface_app{direction="egress"}`,
		},
		// Per-hop ingress octet rate, direction=ingress only.
		{
			Record: "app:if_ingress_octets:rate5m",
			Expr:   "rate(ifHCInOctets[5m]) * " + keys + " " + gr + ` promhash_interface_app{direction="ingress"}`,
		},
		// Interface capacity in bits/s. ifHighSpeed is in Mbit/s, so multiply by
		// 1e6. Guard against bogus 0 capacity by selecting only ifHighSpeed > 0:
		// the comparison without `bool` filters samples, so interfaces reporting
		// 0 capacity (or no ifHighSpeed at all) produce no capacity series and
		// therefore no util series, avoiding div-by-zero infinities downstream.
		//
		// Joins the RAW mapping so the result carries direction: one capacity
		// series per mapped (interface, app, direction). The duplication for a
		// transit interface is intentional — it lets the utilization rules below
		// divide one-to-one on the full label set, giving BOTH an egress and an
		// ingress utilization where both directions are mapped.
		{
			Record: "app:if_capacity_bps",
			Expr:   "(ifHighSpeed > 0) * 1e6 * " + keys + " " + gr + " promhash_interface_app",
		},
		// Operational up-state as a 0/1 gauge. Parenthesize the `== bool 1` so it
		// binds to ifOperStatus and resolves to a 0/1 value BEFORE the join
		// multiply. Collapsed mapping: a down physical interface is represented
		// by exactly one oper-up series per (interface, app) and is not
		// double-counted by app:path_hops_down:count.
		{
			Record: "app:if_oper_up:state",
			Expr:   "(ifOperStatus == bool 1) * " + keys + " " + gr + " " + collapsed,
		},
		// Error and discard rates. IF-MIB has no HC variants for these; the
		// 32-bit counters are safe here — wrapping inside one scrape interval
		// would need >71M events/s at a 60s interval, far beyond any real error
		// rate (octets are a different story, hence ifHC*Octets above).
		// Direction-agnostic facts → collapsed mapping.
		{
			Record: "app:if_in_errors:rate5m",
			Expr:   "rate(ifInErrors[5m]) * " + keys + " " + gr + " " + collapsed,
		},
		{
			Record: "app:if_out_errors:rate5m",
			Expr:   "rate(ifOutErrors[5m]) * " + keys + " " + gr + " " + collapsed,
		},
		{
			Record: "app:if_in_discards:rate5m",
			Expr:   "rate(ifInDiscards[5m]) * " + keys + " " + gr + " " + collapsed,
		},
		{
			Record: "app:if_out_discards:rate5m",
			Expr:   "rate(ifOutDiscards[5m]) * " + keys + " " + gr + " " + collapsed,
		},
		// Firing alerts touching a hop. ALERTS fans M alerts × N apps per
		// interface, which PromQL vector matching cannot express directly (the
		// one side of group_right must be unique per join key). Collapse the
		// alert multiplicity FIRST with count by(<join labels>), then fan the
		// per-interface count out to apps. Detail (which alertnames) belongs to
		// dashboards/Alertmanager, not this series.
		{
			Record: "app:if_alerts_firing:count",
			Expr:   "count by(" + joinKeyLabels(jk) + `)(ALERTS{alertstate="firing"}) * ` + keys + " " + gr + " " + collapsed,
		},
		// Link utilization ratio: bytes/s → bits/s (×8) over capacity bits/s.
		// Two rules share the record name — one per direction — and their output
		// label sets are disjoint (direction="egress" vs direction="ingress"), so
		// the series never collide. Each division matches one-to-one on the FULL
		// label set (capacity carries direction, see app:if_capacity_bps), which
		// is what makes an ingress-declared hop get a utilization series at all.
		// Because capacity has no series where ifHighSpeed is absent/zero, the
		// division yields no series there either (rather than +Inf), so the
		// ratio is implicitly capacity-gated.
		{
			Record: "app:if_util:ratio",
			Expr:   "app:if_egress_octets:rate5m * 8 / app:if_capacity_bps",
		},
		{
			Record: "app:if_util:ratio",
			Expr:   "app:if_ingress_octets:rate5m * 8 / app:if_capacity_bps",
		},
		// Per-path (per app,service) worst-hop utilization. MAX, never sum/avg:
		// a path is only as healthy as its most-congested hop. Spans both
		// directions since app:if_util:ratio now carries direction.
		{
			Record: "app:path_util_max:ratio",
			Expr:   "max by(app, service)(app:if_util:ratio)",
		},
		// Per-path minimum oper-up state: 0 if ANY hop on the path is down.
		{
			Record: "app:path_oper_up_min:state",
			Expr:   "min by(app, service)(app:if_oper_up:state)",
		},
		// Per-path count of hops currently down. sum(1 - state) — NOT
		// count(state == 0) — so a fully-healthy path reads as an explicit 0
		// instead of an absent series. With this shape, "no data" cleanly means
		// the pipeline is broken, never "everything is fine".
		{
			Record: "app:path_hops_down:count",
			Expr:   "sum by(app, service)(1 - app:if_oper_up:state)",
		},
		// Per-path error/discard rollups: in + out summed per (app, service).
		// NOT `sum(in or out)` — PromQL set operators match label sets IGNORING
		// the metric name, and the per-hop in/out series differ only by name, so
		// `or` would silently drop the out side. Instead: sum each side, add
		// them, and `or`-fall back to either side alone so a hop exposing only
		// one of the two counters still contributes (`+` alone would drop
		// groups present on a single side).
		{
			Record: "app:path_errors:rate5m",
			Expr: "(sum by(app, service)(app:if_in_errors:rate5m) + sum by(app, service)(app:if_out_errors:rate5m))" +
				" or sum by(app, service)(app:if_in_errors:rate5m)" +
				" or sum by(app, service)(app:if_out_errors:rate5m)",
		},
		{
			Record: "app:path_discards:rate5m",
			Expr: "(sum by(app, service)(app:if_in_discards:rate5m) + sum by(app, service)(app:if_out_discards:rate5m))" +
				" or sum by(app, service)(app:if_in_discards:rate5m)" +
				" or sum by(app, service)(app:if_out_discards:rate5m)",
		},
		// Per-path firing-alert rollup: total alerts currently touching any hop.
		{
			Record: "app:path_alerts_firing:count",
			Expr:   "sum by(app, service)(app:if_alerts_firing:count)",
		},
	}
}

// ruleGroupsDoc is the top-level shape of a Prometheus rules file: a groups:
// list, each with a name and a rules: list of record/expr pairs.
type ruleGroupsDoc struct {
	Groups []ruleGroupDoc `yaml:"groups"`
}

type ruleGroupDoc struct {
	Name  string           `yaml:"name"`
	Rules []pathHealthRule `yaml:"rules"`
}

// PathHealthRules returns the static, app-independent Prometheus recording-rule
// group (group name promhash_path_health) as YAML. The rules join raw SNMP
// counters against the bounded promhash_interface_app{…}=1 mapping series via
// group_right(), so app/service/device/ifName labels fan out onto the counters.
// The counter is the LEFT ("one") operand and the mapping series is the RIGHT
// ("many") operand, because a shared physical interface can map to multiple apps.
//
// jk selects the join column: JoinByComposite joins on(iface); JoinByIfName
// joins on(instance, ifName). The rule set is otherwise identical and contains
// no app-specific selectors — it is rendered once for the whole deployment.
//
// The returned string is valid Prometheus rules YAML (a groups: document with
// one group and a rules: list).
func PathHealthRules(jk JoinKey) string {
	doc := ruleGroupsDoc{
		Groups: []ruleGroupDoc{
			{
				Name:  pathHealthGroupName,
				Rules: pathHealthRules(jk),
			},
		},
	}

	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	// yaml.Encoder.Encode never errors writing to a strings.Builder (no I/O
	// failure path), and the input is a fixed, marshalable struct.
	_ = enc.Encode(doc)
	_ = enc.Close()
	return b.String()
}

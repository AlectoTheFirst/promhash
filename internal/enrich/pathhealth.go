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

// joinKeys renders the on(...) join-key clause used by the group_left rules for
// the given JoinKey. JoinByComposite joins on the synthesized composite iface
// label; JoinByIfName joins on (instance, ifName).
func joinKeys(jk JoinKey) string {
	switch jk {
	case JoinByIfName:
		return "on(instance, ifName)"
	default: // JoinByComposite
		return "on(iface)"
	}
}

// pathHealthRules builds the ordered slice of recording rules for the path-health
// group. The rules are app-independent: they join the raw SNMP counter firehose
// against RA1's bounded promhash_interface_app{…}=1 mapping series via group_right,
// fanning app/service/device/ifName (and the rest of the mapping's identity
// labels) onto the counters without ever stamping an app label on the raw series
// themselves.
//
// The join clause (on(...)) is derived from jk via joinKeys. The counter is the
// LEFT ("one") operand and the bounded mapping series is the RIGHT ("many")
// operand under group_right(): a single physical interface can be mapped to N
// apps, so the mapping must be the many side or the join would reject the
// duplicate-on-one-side match group. The result therefore inherits the mapping's
// full label set (app/service/device/ifName/iface/instance/ifIndex/direction),
// and the empty group_right() clause keeps the join-key labels out of the group
// clause (so JoinByIfName, which joins on(instance, ifName), does not put ifName
// in both ON and GROUP).
//
// This slice is the single source of truth for both PathHealthRules' YAML
// rendering and any test that wishes to recover the exact exprs.
func pathHealthRules(jk JoinKey) []pathHealthRule {
	keys := joinKeys(jk)
	// group_right(): the mapping (RIGHT) is the many side; the counter (LEFT) is
	// the one side. The result base is the mapping's full label set, so a shared
	// link fans one counter out to one result series per mapped app.
	gr := "group_right()"

	return []pathHealthRule{
		// Per-hop egress octet rate, with app/service/device/ifName (and the rest
		// of the mapping identity) fanned on from the mapping series for
		// direction=egress only.
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
		// This rule is direction-agnostic, but a transit iface emits BOTH an
		// ingress and an egress mapping series, so joining against the raw mapping
		// would yield two identical capacity series per (iface, app). Collapse the
		// direction multiplicity with max without(direction)(…) so there is exactly
		// one capacity series per physical (iface, app).
		{
			Record: "app:if_capacity_bps",
			Expr:   "(ifHighSpeed > 0) * 1e6 * " + keys + " " + gr + " max without(direction)(promhash_interface_app)",
		},
		// Operational up-state as a 0/1 gauge. Parenthesize the `== bool 1` so it
		// binds to ifOperStatus and resolves to a 0/1 value BEFORE the join
		// multiply; otherwise operator precedence would fold the bool comparison
		// across the whole expression.
		//
		// Direction-agnostic like capacity: collapse the ingress/egress mapping
		// multiplicity with max without(direction)(…) so a down physical interface
		// is represented by exactly one oper-up series per (iface, app) and is not
		// double-counted by app:path_hops_down:count.
		{
			Record: "app:if_oper_up:state",
			Expr:   "(ifOperStatus == bool 1) * " + keys + " " + gr + " max without(direction)(promhash_interface_app)",
		},
		// Link utilization ratio: egress bytes/s → bits/s (×8) over capacity bits/s.
		// Because app:if_capacity_bps has no series where capacity is absent/zero,
		// the division yields no series there either (rather than +Inf), so the
		// ratio is implicitly capacity-gated.
		//
		// ignoring(direction): the egress-rate numerator carries direction="egress"
		// (from its group_right join against promhash_interface_app{direction="egress"}),
		// but app:if_capacity_bps has no direction label (it was collapsed via
		// max without(direction)(…) to avoid duplicating capacity for ingress/egress).
		// Without ignoring(direction), Prometheus default one-to-one matching requires
		// identical label sets and the division yields ZERO series. The result is
		// direction-less, which is correct: utilization is a per-interface property.
		{
			Record: "app:if_util:ratio",
			Expr:   "app:if_egress_octets:rate5m * 8 / ignoring(direction) app:if_capacity_bps",
		},
		// Per-path (per app,service) worst-hop utilization. MAX, never sum/avg:
		// a path is only as healthy as its most-congested hop.
		{
			Record: "app:path_util_max:ratio",
			Expr:   "max by(app, service)(app:if_util:ratio)",
		},
		// Per-path minimum oper-up state: 0 if ANY hop on the path is down.
		{
			Record: "app:path_oper_up_min:state",
			Expr:   "min by(app, service)(app:if_oper_up:state)",
		},
		// Per-path count of hops currently down. The `== 0` comparison is a
		// FILTER (no `bool`), so only down hops (value 0) survive into the count;
		// `== bool 0` would instead map every hop to 0/1 and count ALL hops.
		{
			Record: "app:path_hops_down:count",
			Expr:   "count by(app, service)(app:if_oper_up:state == 0)",
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
// group_left, so app/service/device/ifName labels fan out onto the counters.
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

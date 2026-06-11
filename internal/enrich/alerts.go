package enrich

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// pathHealthAlertsGroupName is the name of the alerting-rule group emitted by
// PathHealthAlerts.
const pathHealthAlertsGroupName = "promhash_path_health_alerts"

// alertRule is one alerting rule in the path-health alerts group.
type alertRule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

// alertGroupsDoc is the top-level shape of a Prometheus alerting-rules file.
type alertGroupsDoc struct {
	Groups []alertGroupDoc `yaml:"groups"`
}

type alertGroupDoc struct {
	Name  string      `yaml:"name"`
	Rules []alertRule `yaml:"rules"`
}

// pathHealthAlerts builds the alerting rules for the projection pipeline and
// the per-path series it produces.
//
// The pipeline's signature failure mode is SILENT EMPTINESS: a mapping that
// stops being ingested, a remote_write feed that stalls, or a join key that no
// longer intersects the counters all result in rules that evaluate to nothing
// while every process involved looks healthy. The first four alerts exist to
// turn each of those silent states into a page:
//
//   - PromhashMappingAbsent / PromhashMappingScrapeDown — the mapping series
//     vanished (server down, scrape broken). Every path-health rule joins
//     against it, so all app series stop within the staleness window.
//   - PromhashCountersStale — the raw-counter remote_write feed from the main
//     Prometheus has stalled; rules evaluate at wall-clock "now" and go empty
//     once the data lags past the rate window.
//   - PromhashMappingDrift — mapping rows whose join key matches no counter
//     series: a renamed/renumbered/retired interface, or a label-schema drift
//     between the mapping and the counters. This is what converts the
//     "enrich ran against a stale catalog" window from silent mis-attribution
//     into an explicit signal.
//
// The remaining alerts cover the path-health outputs themselves. Severities are
// deliberately conservative (warning) for path conditions: a down hop may be a
// redundant ECMP candidate — "redundancy lost", not necessarily an outage —
// because candidate paths are flattened until flow ingestion lands.
func pathHealthAlerts(jk JoinKey) []alertRule {
	keys := joinKeys(jk)

	return []alertRule{
		{
			Alert: "PromhashMappingAbsent",
			Expr:  "absent(promhash_interface_app)",
			For:   "5m",
			Labels: map[string]string{
				"severity": "critical",
			},
			Annotations: map[string]string{
				"summary":     "promhash mapping series absent — all path-health rules evaluate to nothing",
				"description": "promhash_interface_app is not present in the evaluator. The mapping scrape is broken or the mapping file is empty; every app:* path-health series has stopped.",
			},
		},
		{
			Alert: "PromhashMappingScrapeDown",
			Expr:  `up{job="promhash-mapping"} == 0`,
			For:   "5m",
			Labels: map[string]string{
				"severity": "critical",
			},
			Annotations: map[string]string{
				"summary":     "promhash mapping scrape target down",
				"description": "The promhash-mapping scrape job cannot reach the mapping server. Once the mapping series go stale the path-health rules evaluate to nothing.",
			},
		},
		{
			Alert: "PromhashCountersStale",
			Expr:  "time() - max(timestamp(ifHCInOctets)) > 300",
			For:   "5m",
			Labels: map[string]string{
				"severity": "critical",
			},
			Annotations: map[string]string{
				"summary":     "raw counter feed stale — remote_write from the main Prometheus has stalled",
				"description": "The newest ifHCInOctets sample is older than 5 minutes. The main Prometheus remote_write feed is lagging or stopped; app:* series will gap until it recovers.",
			},
		},
		{
			Alert: "PromhashMappingDrift",
			Expr:  "count(promhash_interface_app unless " + keys + " ifHCInOctets) > 0",
			For:   "30m",
			Labels: map[string]string{
				"severity": "warning",
			},
			Annotations: map[string]string{
				"summary":     "mapping rows match no counter series — stale or mis-keyed mapping",
				"description": "{{ $value }} mapping row(s) have a join key that intersects no ifHCInOctets series. Causes: interface renamed/renumbered/retired since the last promhash-enrich run, or a label mismatch between mapping and counters. Re-run promhash-catalog and promhash-enrich.",
			},
		},
		{
			Alert: "PromhashPathHopDown",
			Expr:  "app:path_hops_down:count > 0",
			For:   "5m",
			Labels: map[string]string{
				"severity": "warning",
			},
			Annotations: map[string]string{
				"summary":     "{{ $labels.app }}: {{ $value }} hop(s) down on declared path",
				"description": "At least one interface on the declared path of {{ $labels.app }}/{{ $labels.service }} is oper-down. Candidate paths are flattened: a down hop may be a redundant ECMP candidate (redundancy lost), not necessarily a traffic-affecting outage.",
			},
		},
		{
			Alert: "PromhashPathUtilizationHigh",
			Expr:  "app:path_util_max:ratio > 0.9",
			For:   "15m",
			Labels: map[string]string{
				"severity": "warning",
			},
			Annotations: map[string]string{
				"summary":     "{{ $labels.app }}: worst-hop utilization {{ $value | humanizePercentage }}",
				"description": "The most-congested hop on the declared path of {{ $labels.app }}/{{ $labels.service }} has exceeded 90% utilization for 15 minutes. Interface counters are whole-link totals, not this app's share.",
			},
		},
		{
			Alert: "PromhashPathErrors",
			Expr:  "app:path_errors:rate5m > 0",
			For:   "15m",
			Labels: map[string]string{
				"severity": "warning",
			},
			Annotations: map[string]string{
				"summary":     "{{ $labels.app }}: sustained interface errors on declared path",
				"description": "Interfaces on the declared path of {{ $labels.app }}/{{ $labels.service }} have reported a non-zero error rate for 15 minutes (sum of in+out errors across hops: {{ $value }}/s).",
			},
		},
	}
}

// PathHealthAlerts returns the static alerting-rule group (group name
// promhash_path_health_alerts) as YAML, covering both the health of the
// projection pipeline itself (mapping present, counter feed fresh, join keys
// intersecting) and the per-path series it produces.
//
// jk must match the JoinKey the recording rules were generated with: the
// mapping-drift alert compares the mapping against the counters using the same
// on(...) clause as the joins it guards.
//
// The returned string is valid Prometheus rules YAML.
func PathHealthAlerts(jk JoinKey) string {
	doc := alertGroupsDoc{
		Groups: []alertGroupDoc{
			{
				Name:  pathHealthAlertsGroupName,
				Rules: pathHealthAlerts(jk),
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

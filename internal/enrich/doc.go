// Package enrich generates the shared-evaluator projection artifacts for the
// curated application set. From the network hops each application's traffic
// traverses, it builds three GitOps artifacts: the bounded
// promhash_interface_app{…}=1 mapping series (exposition text), the static
// app-independent path-health recording-rule group that joins raw SNMP
// counters against the mapping via group_right(), and the rule-evaluating
// Prometheus configuration that scrapes both and remote-writes the resulting
// app-labeled series. The raw infrastructure metrics never gain an app label;
// the fan-out happens only in the recording rules' join.
package enrich

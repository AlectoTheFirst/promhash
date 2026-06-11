// Package enrich generates the shared-evaluator projection artifacts for the
// curated application set. From the network hops each application's traffic
// traverses, it builds four GitOps artifacts: the bounded
// promhash_interface_app{…}=1 mapping series (exposition text), the static
// app-independent path-health recording-rule group that joins raw SNMP
// counters against the mapping via group_right(), the alerting rules that
// guard both the projection pipeline and the resulting path series, and the
// promhash Prometheus configuration (receiver mode: the raw counters arrive
// via remote_write from the main Prometheus; only the mapping is scraped).
// The raw infrastructure metrics never gain an app label; the fan-out happens
// only in the recording rules' join.
package enrich

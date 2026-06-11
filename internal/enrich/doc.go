// Package enrich generates the shared-evaluator projection artifacts for the
// curated application set. It builds three GitOps configuration artifacts:
// the static app-independent path-health recording-rule group that joins raw
// SNMP counters against the bounded promhash_interface_app{…}=1 mapping
// series via group_right(), the alerting rules that guard both the projection
// pipeline and the resulting path series, and the promhash Prometheus
// configuration (receiver mode: raw counters arrive via remote_write from the
// main Prometheus; the mapping is scraped live from promhash-api, which
// renders it with MappingSeries/RenderMappingSeries on every request). The
// raw infrastructure metrics never gain an app label; the fan-out happens
// only in the recording rules' join.
package enrich

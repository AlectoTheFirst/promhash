package enrich

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// EvaluatorOpts holds the parameters for generating the promhash Prometheus
// config shell. The promhash Prometheus is a dedicated remote-write RECEIVER:
// it gets the raw SNMP counters pushed to it by the main Prometheus (which is
// configured, out of band, with one remote_write block pointing at it) and
// must be started with --web.enable-remote-write-receiver. The generated
// config therefore contains NO counters scrape job — only the mapping scrape,
// the rule files, the tenant identity, and the onward remote_write.
type EvaluatorOpts struct {
	// MappingTarget is the host:port serving the rendered mapping.prom
	// exposition text (see RenderMappingSeries). The path-health rules join the
	// raw counters against promhash_interface_app, so the evaluator MUST ingest
	// the mapping series or every rule evaluates to empty. When MappingTarget
	// is empty no mapping scrape job is emitted (the caller is expected to
	// reject that configuration).
	MappingTarget string
	// MappingMetricsPath is the HTTP path of the mapping exposition on
	// MappingTarget. Empty means the default "/mapping.prom".
	MappingMetricsPath string
	// RemoteWriteURL is the URL of the onward remote_write receiver
	// (long-term storage).
	RemoteWriteURL string
	// TenantLabel is stamped as global.external_labels.tenant to identify the
	// deployment in a multi-tenant remote storage.
	TenantLabel string
}

// DefaultMappingMetricsPath is the metrics_path used for the mapping scrape
// job when EvaluatorOpts.MappingMetricsPath is empty.
const DefaultMappingMetricsPath = "/mapping.prom"

// RulesFileName and AlertsFileName are the rule_files entries referenced by
// the generated config; they must match the artifact filenames written next
// to evaluator.yaml.
const (
	RulesFileName  = "path-health.rules.yaml"
	AlertsFileName = "path-health.alerts.yaml"
)

// evaluatorGlobalDoc is the top-level global: block of the Prometheus config.
type evaluatorGlobalDoc struct {
	ExternalLabels map[string]string `yaml:"external_labels"`
}

// evaluatorStaticConfigDoc is one entry in a static_configs list.
type evaluatorStaticConfigDoc struct {
	Targets []string `yaml:"targets"`
}

// evaluatorScrapeConfigDoc is one entry in a scrape_configs list.
type evaluatorScrapeConfigDoc struct {
	JobName       string                     `yaml:"job_name"`
	HonorLabels   bool                       `yaml:"honor_labels,omitempty"`
	MetricsPath   string                     `yaml:"metrics_path,omitempty"`
	StaticConfigs []evaluatorStaticConfigDoc `yaml:"static_configs"`
}

// evaluatorRemoteWriteDoc is one entry in a remote_write list.
type evaluatorRemoteWriteDoc struct {
	URL string `yaml:"url"`
}

// evaluatorConfigDoc is the full top-level structure of the rendered
// promhash Prometheus config.
type evaluatorConfigDoc struct {
	Global       evaluatorGlobalDoc         `yaml:"global"`
	RuleFiles    []string                   `yaml:"rule_files"`
	ScrapeConfig []evaluatorScrapeConfigDoc `yaml:"scrape_configs"`
	RemoteWrite  []evaluatorRemoteWriteDoc  `yaml:"remote_write"`
}

// SharedEvaluatorConfig renders the promhash Prometheus config that:
//   - stamps opts.TenantLabel as global.external_labels.tenant
//   - scrapes the mapping exposition (promhash_interface_app) from
//     opts.MappingTarget (job_name "promhash-mapping") with honor_labels: true,
//     so the mapping's identity labels (instance, ifIndex, iface) survive the
//     scrape instead of being rewritten to the mapping server's address — the
//     path-health joins depend on them
//   - loads path-health.rules.yaml and path-health.alerts.yaml via rule_files
//   - remote_writes the results to opts.RemoteWriteURL
//
// The raw counters are NOT scraped: they arrive via remote_write from the main
// Prometheus (receiver mode). Because remote-written samples are never
// relabeled by the receiver, the config emits no relabel configuration at all;
// with the ifname join key none is needed (the counters already carry
// instance/ifName), and the composite join key is only usable when the SENDER
// synthesizes the iface label at scrape time.
//
// The returned string is valid Prometheus YAML.
func SharedEvaluatorConfig(opts EvaluatorOpts) string {
	scrapes := []evaluatorScrapeConfigDoc{}
	if opts.MappingTarget != "" {
		path := opts.MappingMetricsPath
		if path == "" {
			path = DefaultMappingMetricsPath
		}
		scrapes = append(scrapes, evaluatorScrapeConfigDoc{
			JobName:     "promhash-mapping",
			HonorLabels: true,
			MetricsPath: path,
			StaticConfigs: []evaluatorStaticConfigDoc{
				{Targets: []string{opts.MappingTarget}},
			},
		})
	}

	doc := evaluatorConfigDoc{
		Global: evaluatorGlobalDoc{
			ExternalLabels: map[string]string{
				"tenant": opts.TenantLabel,
			},
		},
		RuleFiles:    []string{RulesFileName, AlertsFileName},
		ScrapeConfig: scrapes,
		RemoteWrite:  []evaluatorRemoteWriteDoc{{URL: opts.RemoteWriteURL}},
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

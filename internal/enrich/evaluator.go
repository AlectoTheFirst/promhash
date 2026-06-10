package enrich

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// EvaluatorOpts holds the parameters for generating a shared-evaluator
// rule-evaluating Prometheus config. It replaces the old per-app
// federation/tenant model with a single scrape + single remote_write
// architecture.
type EvaluatorOpts struct {
	// ScrapeTarget is the host:port (or scheme://host:port) of the raw-counters
	// endpoint to scrape.
	ScrapeTarget string
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
	// RemoteWriteURL is the URL of the remote_write receiver.
	RemoteWriteURL string
	// TenantLabel is stamped as global.external_labels.tenant to identify the
	// deployment in a multi-tenant remote storage.
	TenantLabel string
	// JoinKey controls whether iface-synthesis metric_relabelling is emitted.
	// JoinByComposite: synthesize iface from [instance, ifIndex] with separator ":".
	// JoinByIfName: no iface synthesis (rules join on instance,ifName directly).
	JoinKey JoinKey
}

// DefaultMappingMetricsPath is the metrics_path used for the mapping scrape
// job when EvaluatorOpts.MappingMetricsPath is empty.
const DefaultMappingMetricsPath = "/mapping.prom"

// evaluatorGlobalDoc is the top-level global: block of the Prometheus config.
type evaluatorGlobalDoc struct {
	ExternalLabels map[string]string `yaml:"external_labels"`
}

// evaluatorRelabelDoc is one entry in a metric_relabel_configs list.
type evaluatorRelabelDoc struct {
	SourceLabels []string `yaml:"source_labels"`
	Separator    string   `yaml:"separator"`
	TargetLabel  string   `yaml:"target_label"`
	Regex        string   `yaml:"regex"`
	Replacement  string   `yaml:"replacement"`
}

// evaluatorStaticConfigDoc is one entry in a static_configs list.
type evaluatorStaticConfigDoc struct {
	Targets []string `yaml:"targets"`
}

// evaluatorScrapeConfigDoc is one entry in a scrape_configs list.
type evaluatorScrapeConfigDoc struct {
	JobName              string                     `yaml:"job_name"`
	HonorLabels          bool                       `yaml:"honor_labels,omitempty"`
	MetricsPath          string                     `yaml:"metrics_path,omitempty"`
	StaticConfigs        []evaluatorStaticConfigDoc `yaml:"static_configs"`
	MetricRelabelConfigs []evaluatorRelabelDoc      `yaml:"metric_relabel_configs,omitempty"`
}

// evaluatorRemoteWriteDoc is one entry in a remote_write list.
type evaluatorRemoteWriteDoc struct {
	URL string `yaml:"url"`
}

// evaluatorConfigDoc is the full top-level structure of the rendered
// rule-evaluating Prometheus config.
type evaluatorConfigDoc struct {
	Global       evaluatorGlobalDoc        `yaml:"global"`
	RuleFiles    []string                  `yaml:"rule_files"`
	ScrapeConfig []evaluatorScrapeConfigDoc `yaml:"scrape_configs"`
	RemoteWrite  []evaluatorRemoteWriteDoc  `yaml:"remote_write"`
}

// SharedEvaluatorConfig renders ONE rule-evaluating Prometheus config that:
//   - stamps opts.TenantLabel as global.external_labels.tenant
//   - scrapes opts.ScrapeTarget as a single static_configs target (job_name
//     "promhash-evaluator"; never a per-app promhash-fed-* job; no honor_labels)
//   - scrapes the mapping exposition (promhash_interface_app) from
//     opts.MappingTarget (job_name "promhash-mapping") with honor_labels: true,
//     so the mapping's identity labels (instance, ifIndex, iface) survive the
//     scrape instead of being rewritten to the mapping server's address — the
//     path-health joins depend on them
//   - in JoinByComposite mode: synthesizes the iface label from [instance,
//     ifIndex] via metric_relabel_configs (separator ":", regex "(.+)",
//     replacement "${1}") on the counters job only (the mapping exposition
//     already carries an explicit iface label)
//   - loads path-health.rules.yaml via rule_files
//   - remote_writes to opts.RemoteWriteURL
//
// The returned string is valid rule-evaluating Prometheus YAML. It replaces the
// old per-app federation/tenant scrape model.
func SharedEvaluatorConfig(opts EvaluatorOpts) string {
	scrape := evaluatorScrapeConfigDoc{
		JobName: "promhash-evaluator",
		StaticConfigs: []evaluatorStaticConfigDoc{
			{Targets: []string{opts.ScrapeTarget}},
		},
	}

	if opts.JoinKey == JoinByComposite {
		scrape.MetricRelabelConfigs = []evaluatorRelabelDoc{
			{
				SourceLabels: []string{"instance", "ifIndex"},
				Separator:    ":",
				TargetLabel:  "iface",
				Regex:        "(.+)",
				Replacement:  "${1}",
			},
		}
	}

	scrapes := []evaluatorScrapeConfigDoc{scrape}
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
		RuleFiles:    []string{"path-health.rules.yaml"},
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

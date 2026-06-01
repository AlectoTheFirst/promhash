// internal/enrich/tenant.go
package enrich

import "fmt"

// TenantScrapeConfig renders the per-app federation scrape job (as YAML) that
// pulls only this app's slice from the main Prometheus into the curated tenant.
// It honors source labels and federates the series selected by match; mainProm
// may include an http:// or https:// scheme, which is stripped to a host:port
// scrape target.
func TenantScrapeConfig(app, mainProm, match string) string {
	return fmt.Sprintf(`scrape_configs:
  - job_name: promhash-fed-%s
    honor_labels: true
    metrics_path: /federate
    params:
      'match[]': ['%s']
    static_configs:
      - targets: ['%s']
`, app, match, hostPort(mainProm))
}

func hostPort(u string) string {
	s := u
	for _, p := range []string{"http://", "https://"} {
		if len(s) >= len(p) && s[:len(p)] == p {
			s = s[len(p):]
		}
	}
	return s
}

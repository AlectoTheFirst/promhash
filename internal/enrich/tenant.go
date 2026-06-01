// internal/enrich/tenant.go
package enrich

import "fmt"

// TenantScrapeConfig is the per-app federation scrape job: pull only this app's
// slice from the main Prometheus into the curated tenant.
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

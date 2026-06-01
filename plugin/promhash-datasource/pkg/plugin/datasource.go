// Package plugin implements the Grafana backend datasource for promhash,
// translating Grafana queries, health checks, and variable/resource calls
// into HTTP requests against the promhash API.
package plugin

import (
	"context"
	"net/http"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// Datasource is a Grafana backend datasource backed by the promhash HTTP API.
// It holds the API base URL and a shared HTTP client used for all upstream calls.
type Datasource struct {
	apiURL string
	hc     *http.Client
}

// NewDatasource returns a Datasource that talks to the promhash API at apiURL,
// using an HTTP client with a fixed 15-second request timeout.
func NewDatasource(apiURL string) *Datasource {
	return &Datasource{apiURL: apiURL, hc: &http.Client{Timeout: 15 * time.Second}}
}

// CheckHealth reports datasource health by issuing a GET to the upstream
// "/apps" endpoint. Any request error or non-200 response yields an error
// status; a 200 response reports an OK status.
func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+"/apps", nil)
	if err != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		// Transport error: resp is nil, nothing to close.
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "connected"}, nil
}

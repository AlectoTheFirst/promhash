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
// It holds the API base URL, the Bearer token presented on every upstream
// call, and a shared HTTP client.
type Datasource struct {
	apiURL   string
	apiToken string
	hc       *http.Client
}

// NewDatasource returns a Datasource that talks to the promhash API at apiURL,
// using an HTTP client with a fixed 15-second request timeout. apiToken is
// sent as "Authorization: Bearer <token>" on every upstream request —
// promhash-api requires one unless it runs with -insecure-no-auth. An empty
// token sends no header.
func NewDatasource(apiURL, apiToken string) *Datasource {
	return &Datasource{apiURL: apiURL, apiToken: apiToken, hc: &http.Client{Timeout: 15 * time.Second}}
}

// newReq builds an upstream GET request with the configured Bearer token
// attached. Every upstream call goes through here so authentication can never
// be forgotten on a new endpoint.
func (d *Datasource) newReq(ctx context.Context, path string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+path, nil)
	if err != nil {
		return nil, err
	}
	if d.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.apiToken)
	}
	return req, nil
}

// CheckHealth reports datasource health by issuing a GET to the upstream
// "/apps" endpoint. A 401 is reported as a token problem (distinct from
// unreachability) so misconfiguration is diagnosable from the datasource
// settings page; any other request error or non-200 yields a generic error
// status.
func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	req, err := d.newReq(ctx, "/apps")
	if err != nil {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		// Transport error: resp is nil, nothing to close.
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API rejected the token (401) — set a valid API token in the datasource settings"}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "connected"}, nil
}

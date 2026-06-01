package plugin

import (
	"context"
	"net/http"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type Datasource struct {
	apiURL string
	hc     *http.Client
}

func NewDatasource(apiURL string) *Datasource {
	return &Datasource{apiURL: apiURL, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+"/apps", nil)
	resp, err := d.hc.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
	}
	defer resp.Body.Close()
	return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "connected"}, nil
}

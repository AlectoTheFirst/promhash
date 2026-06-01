package plugin

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// CallResource serves Grafana variable queries by proxying to the promhash API.
// It supports the path "apps" (mapped to "/apps") and "path_interfaces/<app>"
// (mapped to "/apps/<app>/path"); any other path returns HTTP 404. The upstream
// response body is forwarded verbatim.
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	var upstream string
	switch {
	case req.Path == "apps":
		upstream = "/apps"
	case strings.HasPrefix(req.Path, "path_interfaces/"):
		upstream = "/apps/" + strings.TrimPrefix(req.Path, "path_interfaces/") + "/path"
	default:
		return sender.Send(&backend.CallResourceResponse{Status: http.StatusNotFound})
	}
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+upstream, nil)
	resp, err := d.hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return sender.Send(&backend.CallResourceResponse{Status: http.StatusOK, Body: body})
}

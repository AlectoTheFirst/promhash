package plugin

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// CallResource serves Grafana variable queries by proxying to the promhash API.
// It supports the path "apps" (mapped to "/apps", a JSON array of app names)
// and "path_interfaces/<app>" (mapped to "/apps/<app>/ifaces", a JSON array of
// composite "instance:ifIndex" selectors ready for an iface=~"$iface" panel
// filter); any other path returns HTTP 404. The upstream response body is
// forwarded verbatim.
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
	var upstream string
	switch {
	case req.Path == "apps":
		upstream = "/apps"
	case strings.HasPrefix(req.Path, "path_interfaces/"):
		upstream = "/apps/" + url.PathEscape(strings.TrimPrefix(req.Path, "path_interfaces/")) + "/ifaces"
	default:
		return sender.Send(&backend.CallResourceResponse{Status: http.StatusNotFound})
	}
	r, err := d.newReq(ctx, upstream)
	if err != nil {
		return err
	}
	resp, err := d.hc.Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	return sender.Send(&backend.CallResourceResponse{Status: resp.StatusCode, Body: body})
}

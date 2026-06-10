package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	"github.com/grafana/grafana-plugin-sdk-go/data"
)

type query struct{ QueryType, App, Device, IfName string }
type hop struct {
	Device       string `json:"device"`
	IfName       string `json:"ifName"`
	MetricIfName string `json:"metricIfName"`
	Instance     string `json:"instance"`
	Direction    string `json:"direction"`
	IfIndex      int    `json:"ifIndex"`
}

// QueryData executes each query in req, decoding its JSON model and dispatching
// to runQuery. Errors for an individual query are reported in that query's
// response under its RefID rather than aborting the whole batch.
func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
	resp := backend.NewQueryDataResponse()
	for _, q := range req.Queries {
		var qm query
		if err := json.Unmarshal(q.JSON, &qm); err != nil {
			resp.Responses[q.RefID] = backend.ErrDataResponse(backend.StatusBadRequest, err.Error())
			continue
		}
		frame, err := d.runQuery(ctx, qm)
		if err != nil {
			resp.Responses[q.RefID] = backend.ErrDataResponse(backend.StatusInternal, err.Error())
			continue
		}
		resp.Responses[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
	}
	return resp, nil
}

func (d *Datasource) runQuery(ctx context.Context, q query) (*data.Frame, error) {
	switch q.QueryType {
	case "app_path":
		var hops []hop
		if err := d.getJSON(ctx, "/apps/"+url.PathEscape(q.App)+"/path", &hops); err != nil {
			return nil, err
		}
		dev := make([]string, len(hops))
		iface := make([]string, len(hops))
		idx := make([]int64, len(hops))
		dir := make([]string, len(hops))
		for i, h := range hops {
			dev[i] = h.Device
			iface[i] = h.MetricIfName
			idx[i] = int64(h.IfIndex)
			dir[i] = h.Direction
		}
		return data.NewFrame("app_path",
			data.NewField("device", nil, dev), data.NewField("ifName", nil, iface),
			data.NewField("ifIndex", nil, idx), data.NewField("direction", nil, dir)), nil
	default: // impact / interface_apps
		// The API wraps the rows: {"interface": "...", "impact": [...], "note"?: "..."}.
		var resp struct {
			Impact []map[string]string `json:"impact"`
		}
		qs := url.Values{"device": {q.Device}, "ifName": {q.IfName}}
		if err := d.getJSON(ctx, "/interface-apps?"+qs.Encode(), &resp); err != nil {
			return nil, err
		}
		rows := resp.Impact
		app := make([]string, len(rows))
		svc := make([]string, len(rows))
		cust := make([]string, len(rows))
		owner := make([]string, len(rows))
		crit := make([]string, len(rows))
		for i, r := range rows {
			app[i] = r["app"]
			svc[i] = r["service"]
			cust[i] = r["customer"]
			owner[i] = r["owner"]
			crit[i] = r["criticality"]
		}
		return data.NewFrame("impact",
			data.NewField("app", nil, app), data.NewField("service", nil, svc),
			data.NewField("customer", nil, cust), data.NewField("owner", nil, owner),
			data.NewField("criticality", nil, crit)), nil
	}
}

func (d *Datasource) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("upstream %s returned status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

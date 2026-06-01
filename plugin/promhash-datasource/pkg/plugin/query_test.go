package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestQueryAppPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`[{"device":"rtr-core-1","metricIfName":"Te0/1/2","ifIndex":42,"direction":"egress"}]`))
	}))
	defer srv.Close()
	ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
	raw, _ := json.Marshal(map[string]string{"queryType": "app_path", "app": "payments"})
	resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatal(dr.Error)
	}
	if len(dr.Frames) != 1 || dr.Frames[0].Rows() != 1 {
		t.Fatalf("frames %+v", dr.Frames)
	}
}

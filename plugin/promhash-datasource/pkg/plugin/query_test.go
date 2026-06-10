package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	f := dr.Frames[0]
	if f.Name != "app_path" {
		t.Fatalf("frame name = %q, want app_path", f.Name)
	}
	// ifName field maps from metricIfName in the hop payload.
	ifField, idx := f.FieldByName("ifName")
	if idx < 0 {
		t.Fatalf("no ifName field in frame")
	}
	if got := ifField.At(0).(string); got != "Te0/1/2" {
		t.Fatalf("ifName[0] = %q, want Te0/1/2", got)
	}
	dirField, idx := f.FieldByName("direction")
	if idx < 0 {
		t.Fatalf("no direction field in frame")
	}
	if got := dirField.At(0).(string); got != "egress" {
		t.Fatalf("direction[0] = %q, want egress", got)
	}
}

func TestQueryImpactDefault(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		// The real API wraps the rows; the plugin must unwrap "impact".
		w.Write([]byte(`{"interface":"rtr-core-1/tengige0/1/2","impact":[{"app":"payments","service":"checkout","customer":"acme","owner":"team-pay","criticality":"tier-1"}]}`))
	}))
	defer srv.Close()
	ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
	raw, _ := json.Marshal(map[string]string{
		"queryType": "impact", "device": "rtr-core-1", "ifName": "Te0/1/2"})
	resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatal(dr.Error)
	}
	// Hits the new query-param endpoint, not the old path-segment route.
	if gotPath != "/interface-apps" {
		t.Fatalf("upstream path = %q, want /interface-apps", gotPath)
	}
	vals, _ := url.ParseQuery(gotQuery)
	if vals.Get("device") != "rtr-core-1" || vals.Get("ifName") != "Te0/1/2" {
		t.Fatalf("query params = %q", gotQuery)
	}
	if len(dr.Frames) != 1 || dr.Frames[0].Rows() != 1 {
		t.Fatalf("frames %+v", dr.Frames)
	}
	f := dr.Frames[0]
	if f.Name != "impact" {
		t.Fatalf("frame name = %q, want impact", f.Name)
	}
	appField, idx := f.FieldByName("app")
	if idx < 0 {
		t.Fatalf("no app field in frame")
	}
	if got := appField.At(0).(string); got != "payments" {
		t.Fatalf("app[0] = %q, want payments", got)
	}
	svcField, idx := f.FieldByName("service")
	if idx < 0 {
		t.Fatalf("no service field in frame")
	}
	if got := svcField.At(0).(string); got != "checkout" {
		t.Fatalf("service[0] = %q, want checkout", got)
	}
	critField, idx := f.FieldByName("criticality")
	if idx < 0 {
		t.Fatalf("no criticality field in frame")
	}
	if got := critField.At(0).(string); got != "tier-1" {
		t.Fatalf("criticality[0] = %q, want tier-1", got)
	}
}

// TestQueryImpactNoPathKnown asserts the wrapped empty-impact response (with
// its "note" field) decodes to an empty frame rather than erroring.
func TestQueryImpactNoPathKnown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"interface":"rtr-x/eth0","impact":[],"note":"no path known"}`))
	}))
	defer srv.Close()
	ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
	raw, _ := json.Marshal(map[string]string{"queryType": "impact", "device": "rtr-x", "ifName": "eth0"})
	resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
		Queries: []backend.DataQuery{{RefID: "A", JSON: raw}}})
	if err != nil {
		t.Fatal(err)
	}
	dr := resp.Responses["A"]
	if dr.Error != nil {
		t.Fatal(dr.Error)
	}
	if len(dr.Frames) != 1 || dr.Frames[0].Rows() != 0 {
		t.Fatalf("want one empty frame, got %+v", dr.Frames)
	}
}

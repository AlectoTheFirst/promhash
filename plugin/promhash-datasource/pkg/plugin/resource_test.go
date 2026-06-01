package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type capSender struct {
	body   []byte
	status int
}

func (c *capSender) Send(r *backend.CallResourceResponse) error {
	c.body = r.Body
	c.status = r.Status
	return nil
}

func TestCallResourceApps(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`["payments","ledger"]`)) }))
	defer srv.Close()
	ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
	cs := &capSender{}
	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Path: "apps"}, cs)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	_ = json.Unmarshal(cs.body, &out)
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
}

func TestCallResourcePathInterfaces(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[{"device":"rtr-core-1","metricIfName":"Te0/1/2"}]`))
	}))
	defer srv.Close()
	ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
	cs := &capSender{}
	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Path: "path_interfaces/payments"}, cs)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/apps/payments/path" {
		t.Fatalf("upstream path = %q, want /apps/payments/path", gotPath)
	}
	if cs.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", cs.status)
	}
	var out []map[string]string
	if err := json.Unmarshal(cs.body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0]["device"] != "rtr-core-1" {
		t.Fatalf("body = %v", out)
	}
}

func TestCallResourceNotFound(t *testing.T) {
	ds := &Datasource{apiURL: "http://unused.invalid", hc: http.DefaultClient}
	cs := &capSender{}
	err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Path: "bogus"}, cs)
	if err != nil {
		t.Fatal(err)
	}
	if cs.status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", cs.status)
	}
}

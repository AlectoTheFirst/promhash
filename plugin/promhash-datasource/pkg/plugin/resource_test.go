package plugin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

type capSender struct{ body []byte }

func (c *capSender) Send(r *backend.CallResourceResponse) error { c.body = r.Body; return nil }

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

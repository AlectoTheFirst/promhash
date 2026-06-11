package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

// tokenRecorder returns a server that records the Authorization header of the
// last request and serves a minimal valid body for every plugin endpoint.
func tokenRecorder(last *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*last = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
}

// TestBearerTokenSentOnAllUpstreamPaths asserts every upstream call path —
// health check, data query, resource call — carries the configured token.
func TestBearerTokenSentOnAllUpstreamPaths(t *testing.T) {
	var got string
	srv := tokenRecorder(&got)
	defer srv.Close()

	ds := NewDatasource(srv.URL, "s3cret")
	ds.hc = srv.Client()

	if _, err := ds.CheckHealth(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("CheckHealth Authorization = %q", got)
	}

	got = ""
	if err := ds.getJSON(context.Background(), "/apps", &[]string{}); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("getJSON Authorization = %q", got)
	}

	got = ""
	sender := backend.CallResourceResponseSenderFunc(func(_ *backend.CallResourceResponse) error { return nil })
	if err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Path: "apps"}, sender); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer s3cret" {
		t.Errorf("CallResource Authorization = %q", got)
	}
}

// TestNoHeaderWithoutToken asserts an empty token sends no Authorization
// header at all (for -insecure-no-auth deployments).
func TestNoHeaderWithoutToken(t *testing.T) {
	var got string
	srv := tokenRecorder(&got)
	defer srv.Close()

	ds := NewDatasource(srv.URL, "")
	ds.hc = srv.Client()
	if _, err := ds.CheckHealth(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Errorf("expected no Authorization header, got %q", got)
	}
}

// TestCheckHealth401ReportsTokenProblem asserts a 401 is surfaced as a token
// misconfiguration, not generic unreachability.
func TestCheckHealth401ReportsTokenProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	ds := NewDatasource(srv.URL, "wrong")
	ds.hc = srv.Client()
	res, err := ds.CheckHealth(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != backend.HealthStatusError || !strings.Contains(res.Message, "token") {
		t.Fatalf("expected token-specific error, got %v %q", res.Status, res.Message)
	}
}

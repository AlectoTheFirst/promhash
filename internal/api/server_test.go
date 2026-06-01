package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/starkweb/promhash/internal/graph"
)

type fakeRepo struct{}

func (fakeRepo) AppPath(_ context.Context, app string, _ time.Time) ([]graph.Hop, error) {
	return []graph.Hop{{Device: "rtr-core-1", MetricIfName: "Te0/1/2", IfIndex: 42, Direction: "egress"}}, nil
}
func (fakeRepo) InterfaceImpact(_ context.Context, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
func (fakeRepo) ListApps(_ context.Context) ([]string, error) { return []string{"payments"}, nil }

func TestAppPathHandler(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/apps/payments/path", nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out) != 1 || out[0]["ifIndex"].(float64) != 42 {
		t.Fatalf("body %s", rec.Body)
	}
}

// TestIfaceAppsHandler exercises the interface-apps endpoint with an ifName
// that contains '/'. The old path-segment route 404'd on such names because a
// single {ifName} wildcard cannot span the slash; the query-param route must
// resolve them correctly and return 200 with the impacted apps.
func TestIfaceAppsHandler(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/interface-apps?device=rtr-core-1&ifName="+url.QueryEscape("tengige0/1/2"), nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	if len(out) != 1 || out[0]["app"] != "payments" {
		t.Fatalf("body %s", rec.Body)
	}
}

// TestAtParamInvalid verifies that an unparseable "at" query param is rejected
// with 400 rather than silently falling back to the current time.
func TestAtParamInvalid(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/impact?device=rtr-core-1&ifName=Gi0&at=notanumber", nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
}

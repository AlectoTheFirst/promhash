package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

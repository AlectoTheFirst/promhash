package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// fakeRepo is the default fake that returns one ImpactRow.
type fakeRepo struct{}

func (fakeRepo) AppPath(_ context.Context, app string, _ time.Time) ([]graph.Hop, error) {
	return []graph.Hop{{Device: "rtr-core-1", MetricIfName: "Te0/1/2", IfIndex: 42, Direction: "egress"}}, nil
}
func (fakeRepo) InterfaceImpact(_ context.Context, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
func (fakeRepo) ListApps(_ context.Context) ([]string, error) { return []string{"payments"}, nil }

// emptyRepo returns a nil slice from InterfaceImpact to exercise the nil→[] guard.
type emptyRepo struct{ fakeRepo }

func (emptyRepo) InterfaceImpact(_ context.Context, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	return nil, nil
}

// recordingRepo records the phash passed to InterfaceImpact.
type recordingRepo struct {
	fakeRepo
	recorded []string
}

func (r *recordingRepo) InterfaceImpact(_ context.Context, p string, _ time.Time) ([]graph.ImpactRow, error) {
	r.recorded = append(r.recorded, p)
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}

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
// Contract update (C1): /interface-apps now returns the wrapped shape
// {"interface":..., "impact":[...]} identical to /impact.
func TestIfaceAppsHandler(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/interface-apps?device=rtr-core-1&ifName="+url.QueryEscape("tengige0/1/2"), nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	impact, ok := out["impact"].([]any)
	if !ok || len(impact) != 1 {
		t.Fatalf("expected impact array with 1 element, body %s", rec.Body)
	}
	row := impact[0].(map[string]any)
	if row["app"] != "payments" {
		t.Fatalf("body %s", rec.Body)
	}
}

// TestImpactAndIfaceAppsIdentical asserts that /impact and /interface-apps
// return byte-identical response bodies for the same device/ifName query.
func TestImpactAndIfaceAppsIdentical(t *testing.T) {
	srv := NewServer(fakeRepo{})
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("tengige0/1/2")

	recImpact := httptest.NewRecorder()
	srv.Mux().ServeHTTP(recImpact, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))
	if recImpact.Code != 200 {
		t.Fatalf("/impact code %d body %s", recImpact.Code, recImpact.Body)
	}

	recIface := httptest.NewRecorder()
	srv.Mux().ServeHTTP(recIface, httptest.NewRequest(http.MethodGet, "/interface-apps"+q, nil))
	if recIface.Code != 200 {
		t.Fatalf("/interface-apps code %d body %s", recIface.Code, recIface.Body)
	}

	if !bytes.Equal(recImpact.Body.Bytes(), recIface.Body.Bytes()) {
		t.Fatalf("/impact and /interface-apps differ:\n  /impact:         %s\n  /interface-apps: %s",
			recImpact.Body, recIface.Body)
	}

	// Also verify the wrapped shape is correct.
	var out map[string]any
	if err := json.Unmarshal(recImpact.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, recImpact.Body)
	}
	if out["interface"] == nil || out["impact"] == nil {
		t.Fatalf("missing wrapped fields, body %s", recImpact.Body)
	}
}

// TestImpactEmptyHasNoteAndNotNull asserts that when InterfaceImpact returns a
// nil slice, both routes emit [] (never null) and include "no path known".
func TestImpactEmptyHasNoteAndNotNull(t *testing.T) {
	srv := NewServer(emptyRepo{})
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("tengige0/1/2")

	for _, path := range []string{"/impact", "/interface-apps"} {
		rec := httptest.NewRecorder()
		srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+q, nil))
		if rec.Code != 200 {
			t.Fatalf("%s code %d body %s", path, rec.Code, rec.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s unmarshal: %v body %s", path, err, rec.Body)
		}
		if out["note"] != "no path known" {
			t.Fatalf("%s: expected note 'no path known', got %v", path, out["note"])
		}
		// Verify raw bytes contain [] not null.
		raw := rec.Body.String()
		if !bytes.Contains(rec.Body.Bytes(), []byte(`"impact":[]`)) {
			t.Fatalf("%s: expected impact:[] in body, got %s", path, raw)
		}
	}
}

// TestImpactBothRoutesSamePHash asserts that both routes pass the same
// resolved phash to InterfaceImpact for the same device/ifName query.
func TestImpactBothRoutesSamePHash(t *testing.T) {
	repo := &recordingRepo{}
	srv := NewServer(repo)
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("tengige0/1/2")

	srv.Mux().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/impact"+q, nil))
	srv.Mux().ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/interface-apps"+q, nil))

	if len(repo.recorded) != 2 {
		t.Fatalf("expected 2 recorded phashes, got %d", len(repo.recorded))
	}
	if repo.recorded[0] != repo.recorded[1] {
		t.Fatalf("/impact phash %q != /interface-apps phash %q",
			repo.recorded[0], repo.recorded[1])
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

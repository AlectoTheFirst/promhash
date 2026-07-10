package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

// canonicalPHash is the phash for the test interface (device rtr-core-1,
// canonical ifName tengige0/1/2). Both the natural name Te0/1/2 and the
// canonical name must resolve to this same value.
var canonicalPHash = phash.Hash(phash.KindIface, "rtr-core-1", "tengige0/1/2")

// testIfaces returns the catalog slice used by fakeRepo. The single interface
// has IfName=tengige0/1/2 (canonical) and MetricIfName=Te0/1/2 (natural),
// so the resolver maps both forms to the same canonical phash.
func testIfaces() []graph.Iface {
	return []graph.Iface{{
		PHash:        canonicalPHash,
		Device:       "rtr-core-1",
		IfName:       "tengige0/1/2",
		MetricIfName: "Te0/1/2",
	}}
}

// fakeRepo is the default fake that returns one ImpactRow.
type fakeRepo struct{}

func (fakeRepo) AppPath(_ context.Context, app string, _ time.Time) ([]graph.Hop, error) {
	return []graph.Hop{{Device: "rtr-core-1", MetricIfName: "Te0/1/2", IfIndex: 42, Direction: "egress"}}, nil
}
func (fakeRepo) InterfaceImpact(_ context.Context, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
func (fakeRepo) InterfaceImpactByInstanceIndex(_ context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
func (fakeRepo) ListApps(_ context.Context) ([]string, error) { return []string{"payments"}, nil }
func (fakeRepo) AppServiceNames(_ context.Context, apps []string) (map[string]string, error) {
	out := make(map[string]string, len(apps))
	for _, app := range apps {
		out[app] = app + "-api"
	}
	return out, nil
}
func (fakeRepo) ListAllInterfaces(_ context.Context) ([]graph.Iface, error) {
	return testIfaces(), nil
}
func (fakeRepo) Ping(_ context.Context) error { return nil }

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

// ambiguousRepo returns two interfaces on one device that both match the ref
// "Te0/1/2": one via canonical IfName, one via MetricIfName — so Resolve yields
// *catalog.AmbiguousError.
type ambiguousRepo struct{ fakeRepo }

func (ambiguousRepo) ListAllInterfaces(_ context.Context) ([]graph.Iface, error) {
	return []graph.Iface{
		{PHash: "if-a", Device: "rtr-dup", IfName: "tengige0/1/2", MetricIfName: "ten-a"},
		{PHash: "if-b", Device: "rtr-dup", IfName: "ten-b", MetricIfName: "Te0/1/2"},
	}, nil
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

// multiHopRepo returns duplicate and transit hops to exercise the composite
// selector dedup in /apps/{app}/ifaces.
type multiHopRepo struct{ fakeRepo }

func (multiHopRepo) AppPath(_ context.Context, _ string, _ time.Time) ([]graph.Hop, error) {
	return []graph.Hop{
		{Device: "rtr-core-1", Instance: "10.0.0.1", IfIndex: 42, Seq: 1, Direction: "egress"},
		{Device: "rtr-core-2", Instance: "10.0.0.2", IfIndex: 7, Seq: 2, Direction: "transit"},
		{Device: "rtr-core-1", Instance: "10.0.0.1", IfIndex: 42, Seq: 3, Direction: "ingress"}, // dup pair
	}, nil
}

// TestAppIfacesHandler asserts /apps/{app}/ifaces returns the deduplicated,
// sorted composite instance:ifIndex selectors for the app's hops.
func TestAppIfacesHandler(t *testing.T) {
	srv := NewServer(multiHopRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps/payments/ifaces", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	var out []string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	want := []string{"10.0.0.1:42", "10.0.0.2:7"}
	if len(out) != len(want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("got %v, want %v", out, want)
		}
	}
}

// TestAppIfacesEmptyIsArray asserts an app with no hops yields [] (never null),
// so a Grafana variable query degrades to an empty option list.
func TestAppIfacesEmptyIsArray(t *testing.T) {
	srv := NewServer(emptyPathRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/apps/unknown/ifaces", nil))
	if rec.Code != 200 {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "[]" {
		t.Fatalf("body = %q, want []", got)
	}
}

// emptyPathRepo returns no hops.
type emptyPathRepo struct{ fakeRepo }

func (emptyPathRepo) AppPath(_ context.Context, _ string, _ time.Time) ([]graph.Hop, error) {
	return nil, nil
}

// TestHealthEndpoints asserts /healthz is always 200 and /readyz reflects the
// repo's Ping result.
func TestHealthEndpoints(t *testing.T) {
	srv := NewServer(fakeRepo{})
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s: code %d, want 200", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	NewServer(downRepo{}).Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with down store: code %d, want 503", rec.Code)
	}
}

// downRepo simulates an unreachable graph store.
type downRepo struct{ fakeRepo }

func (downRepo) Ping(_ context.Context) error { return errors.New("connection refused") }

// TestMetricsEndpoint asserts /metrics serves Prometheus exposition text.
func TestMetricsEndpoint(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatalf("expected go runtime metrics in /metrics output")
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

// TestImpactBothRoutesSamePHash is a regression guard asserting that /impact
// and /interface-apps both resolve the same device/ifName to the identical
// CANONICAL phash. After C2, the resolver drives resolution, so this test
// also guards that neither route regresses to a raw-hash path.
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
	// Both must equal the canonical phash (not a raw hash of the query param).
	if repo.recorded[0] != canonicalPHash {
		t.Fatalf("phash %q != canonical %q", repo.recorded[0], canonicalPHash)
	}
}

// TestImpactResolvesNaturalName verifies that querying with the natural/metric
// ifName (Te0/1/2) is resolved to the canonical phash before hitting the repo.
func TestImpactResolvesNaturalName(t *testing.T) {
	repo := &recordingRepo{}
	srv := NewServer(repo)
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("Te0/1/2")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if len(repo.recorded) != 1 {
		t.Fatalf("expected 1 recorded phash, got %d", len(repo.recorded))
	}
	if repo.recorded[0] != canonicalPHash {
		t.Fatalf("InterfaceImpact called with raw phash %q, want canonical %q",
			repo.recorded[0], canonicalPHash)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	impact, ok := out["impact"].([]any)
	if !ok || len(impact) != 1 {
		t.Fatalf("expected impact array with 1 element, body %s", rec.Body)
	}
}

// TestImpactResolvesCanonicalName verifies that querying with the canonical
// ifName (tengige0/1/2) also resolves to the same canonical phash.
func TestImpactResolvesCanonicalName(t *testing.T) {
	repo := &recordingRepo{}
	srv := NewServer(repo)
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("tengige0/1/2")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if len(repo.recorded) != 1 {
		t.Fatalf("expected 1 recorded phash, got %d", len(repo.recorded))
	}
	if repo.recorded[0] != canonicalPHash {
		t.Fatalf("InterfaceImpact called with raw phash %q, want canonical %q",
			repo.recorded[0], canonicalPHash)
	}
}

// TestImpactUnknownIfaceReturns404 verifies that an unrecognised interface
// reference produces a 404 response whose JSON body includes a non-empty
// "suggestions" list drawn from the catalog.
func TestImpactUnknownIfaceReturns404(t *testing.T) {
	srv := NewServer(fakeRepo{})
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("Zz9/9/9")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	if out["error"] == nil {
		t.Fatalf("missing 'error' field, body %s", rec.Body)
	}
	sug, ok := out["suggestions"].([]any)
	if !ok || len(sug) == 0 {
		t.Fatalf("expected non-empty 'suggestions', body %s", rec.Body)
	}
}

// TestImpactAmbiguousReturns409 verifies that a reference matching more than one
// interface on a device produces a 409 whose JSON body lists the matches.
func TestImpactAmbiguousReturns409(t *testing.T) {
	srv := NewServer(ambiguousRepo{})
	q := "?device=rtr-dup&ifName=" + url.QueryEscape("Te0/1/2")
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body %s", rec.Code, rec.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v body %s", err, rec.Body)
	}
	matches, ok := out["matches"].([]any)
	if !ok || len(matches) < 2 {
		t.Fatalf("expected >=2 'matches', body %s", rec.Body)
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

// accessTrackingRepo embeds fakeRepo and records whether ListAllInterfaces or
// InterfaceImpact was called. Both methods use pointer receivers so the flags
// are visible on the same *accessTrackingRepo after the call.
type accessTrackingRepo struct {
	fakeRepo
	listAllInterfacesCalled bool
	interfaceImpactCalled   bool
}

func (r *accessTrackingRepo) ListAllInterfaces(ctx context.Context) ([]graph.Iface, error) {
	r.listAllInterfacesCalled = true
	return r.fakeRepo.ListAllInterfaces(ctx)
}

func (r *accessTrackingRepo) InterfaceImpact(ctx context.Context, p string, t time.Time) ([]graph.ImpactRow, error) {
	r.interfaceImpactCalled = true
	return r.fakeRepo.InterfaceImpact(ctx, p, t)
}

// TestImpactEmptyDeviceRejected asserts that an empty device param yields 400
// and that no repo methods are called (guard fires before any catalog access).
func TestImpactEmptyDeviceRejected(t *testing.T) {
	repo := &accessTrackingRepo{}
	srv := NewServer(repo)
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact?device=&ifName=Gi0", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body)
	}
	if repo.listAllInterfacesCalled {
		t.Fatal("ListAllInterfaces was called — guard must short-circuit before repo access")
	}
	if repo.interfaceImpactCalled {
		t.Fatal("InterfaceImpact was called — guard must short-circuit before repo access")
	}
}

// TestImpactEmptyIfNameRejected asserts that an empty ifName param yields 400
// and that no repo methods are called.
func TestImpactEmptyIfNameRejected(t *testing.T) {
	repo := &accessTrackingRepo{}
	srv := NewServer(repo)
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact?device=rtr-core-1&ifName=", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body)
	}
	if repo.listAllInterfacesCalled {
		t.Fatal("ListAllInterfaces was called — guard must short-circuit before repo access")
	}
	if repo.interfaceImpactCalled {
		t.Fatal("InterfaceImpact was called — guard must short-circuit before repo access")
	}
}

// TestImpactEmptyDeviceRejectedViaAlias asserts that /interface-apps inherits
// the guard from the impact handler and also returns 400 for an empty device.
func TestImpactEmptyDeviceRejectedViaAlias(t *testing.T) {
	repo := &accessTrackingRepo{}
	srv := NewServer(repo)
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/interface-apps?device=&ifName=Gi0", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 from /interface-apps alias, got %d body %s", rec.Code, rec.Body)
	}
	if repo.listAllInterfacesCalled {
		t.Fatal("ListAllInterfaces was called — guard must short-circuit before repo access")
	}
	if repo.interfaceImpactCalled {
		t.Fatal("InterfaceImpact was called — guard must short-circuit before repo access")
	}
}

// TestAtParamBounds verifies that out-of-range timestamps are rejected with 400
// before any repo method is called, and that in-range timestamps (including the
// lower boundary 0 and absent param) are accepted with 200.
//
// The test drives /impact with a valid device/ifName so the empty-param guard
// (C3) does not fire before at() is evaluated. Out-of-range cases must not
// reach the repo — accessTrackingRepo's flags prove that.
func TestAtParamBounds(t *testing.T) {
	const validQ = "device=rtr-core-1&ifName=" + "tengige0%2F1%2F2"

	tests := []struct {
		name              string
		at                string // empty means omit the param
		wantCode          int
		wantRepoNotCalled bool
	}{
		{
			name:              "negative timestamp rejected",
			at:                "-1",
			wantCode:          http.StatusBadRequest,
			wantRepoNotCalled: true,
		},
		{
			name:              "far-future timestamp rejected",
			at:                "99999999999999",
			wantCode:          http.StatusBadRequest,
			wantRepoNotCalled: true,
		},
		{
			name:     "valid recent timestamp accepted",
			at:       "1700000000",
			wantCode: http.StatusOK,
		},
		{
			name:     "lower boundary zero accepted",
			at:       "0",
			wantCode: http.StatusOK,
		},
		{
			name:     "absent at param accepted",
			at:       "",
			wantCode: http.StatusOK,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &accessTrackingRepo{}
			srv := NewServer(repo)
			u := "/impact?" + validQ
			if tc.at != "" {
				u += "&at=" + tc.at
			}
			rec := httptest.NewRecorder()
			srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))

			if rec.Code != tc.wantCode {
				t.Fatalf("at=%q: got HTTP %d, want %d (body: %s)", tc.at, rec.Code, tc.wantCode, rec.Body)
			}
			if tc.wantRepoNotCalled {
				if repo.listAllInterfacesCalled {
					t.Errorf("at=%q: ListAllInterfaces was called — at-bounds guard must fire before repo access", tc.at)
				}
				if repo.interfaceImpactCalled {
					t.Errorf("at=%q: InterfaceImpact was called — at-bounds guard must fire before repo access", tc.at)
				}
			}
		})
	}
}

// TestImpactWhitespaceOnlyDeviceRejected asserts that a whitespace-only device
// (%20) is treated as empty and rejected with 400.
func TestImpactWhitespaceOnlyDeviceRejected(t *testing.T) {
	repo := &accessTrackingRepo{}
	srv := NewServer(repo)
	rec := httptest.NewRecorder()
	// %20 = a single space
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact?device=%20&ifName=Gi0", nil))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for whitespace-only device, got %d body %s", rec.Code, rec.Body)
	}
	if repo.listAllInterfacesCalled {
		t.Fatal("ListAllInterfaces was called — guard must short-circuit before repo access")
	}
}

// TestImpactExactInstanceIndex verifies that when instance+ifIndex params are
// present the handler uses the exact lookup and returns the wrapped shape.
func TestImpactExactInstanceIndex(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/impact?instance=10.0.0.1:161&ifIndex=42", nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
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
	if impact[0].(map[string]any)["app"] != "payments" {
		t.Fatalf("body %s", rec.Body)
	}
}

// TestImpactExactBadIfIndex verifies a non-integer ifIndex is rejected with 400.
func TestImpactExactBadIfIndex(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/impact?instance=10.0.0.1&ifIndex=notanint", nil)
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body %s", rec.Code, rec.Body)
	}
}

// TestImpactExactPrecedence verifies instance+ifIndex take precedence over
// device+ifName when both are supplied (exact path is used).
func TestImpactExactPrecedence(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/impact?instance=10.0.0.1&ifIndex=42&device=rtr-core-1&ifName=Zz9", nil)
	srv.Mux().ServeHTTP(rec, req)
	// Zz9 would 404 on the fuzzy path; exact path returns 200, proving precedence.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected exact path 200, got %d body %s", rec.Code, rec.Body)
	}
}

// countingCatalogRepo counts ListAllInterfaces calls so tests can prove the
// resolver is cached rather than rebuilt from a full catalog scan per request.
type countingCatalogRepo struct {
	fakeRepo
	listCalls int
}

func (r *countingCatalogRepo) ListAllInterfaces(ctx context.Context) ([]graph.Iface, error) {
	r.listCalls++
	return r.fakeRepo.ListAllInterfaces(ctx)
}

// TestImpactResolverCachedWithinTTL: two name-based impact lookups in quick
// succession must hit the catalog once. Rebuilding the resolver from a full
// ListAllInterfaces scan per request (the old OPT-10 behavior) collapses under
// alert-storm load.
func TestImpactResolverCachedWithinTTL(t *testing.T) {
	repo := &countingCatalogRepo{}
	srv := NewServer(repo)
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("Te0/1/2")

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: code %d body %s", i, rec.Code, rec.Body)
		}
	}
	if repo.listCalls != 1 {
		t.Fatalf("ListAllInterfaces called %d times for 2 requests, want 1 (cached resolver)", repo.listCalls)
	}
}

// TestImpactResolverRefreshesAfterTTL: the cached resolver must expire so
// interfaces added by a later catalog sync become resolvable without an API
// restart. Ten minutes is far past any sane cache TTL.
func TestImpactResolverRefreshesAfterTTL(t *testing.T) {
	repo := &countingCatalogRepo{}
	srv := NewServer(repo)
	clock := time.Now()
	srv.now = func() time.Time { return clock }
	q := "?device=rtr-core-1&ifName=" + url.QueryEscape("Te0/1/2")

	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request: code %d body %s", rec.Code, rec.Body)
	}

	clock = clock.Add(10 * time.Minute)

	rec = httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/impact"+q, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("second request: code %d body %s", rec.Code, rec.Body)
	}
	if repo.listCalls != 2 {
		t.Fatalf("ListAllInterfaces called %d times across the TTL boundary, want 2 (cache must expire)", repo.listCalls)
	}
}

// TestMappingPromHandler: GET /mapping.prom renders the live exposition for
// the apps named in the query param. fakeRepo serves the same single egress
// hop for every app, so two apps yield two mapping rows on one interface
// (the shared-link fan-out).
func TestMappingPromHandler(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mapping.prom?apps=payments,ledger", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain exposition", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`promhash_interface_app{app="payments",service="payments-api"`,
		`promhash_interface_app{app="ledger",service="ledger-api"`,
		`direction="egress"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("exposition missing %q:\n%s", want, body)
		}
	}
	if got := strings.Count(body, "promhash_interface_app{"); got != 2 {
		t.Errorf("expected 2 mapping rows (one per app), got %d:\n%s", got, body)
	}
}

// TestMappingPromRequiresApps: the apps param is mandatory; the curated set is
// an explicit enrich-time decision, never "everything in the graph".
func TestMappingPromRequiresApps(t *testing.T) {
	srv := NewServer(fakeRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mapping.prom", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without apps param, got %d", rec.Code)
	}
}

// TestMappingPromSkipsPathlessApps: an app with no known path contributes no
// rows rather than erroring the whole exposition.
func TestMappingPromSkipsPathlessApps(t *testing.T) {
	srv := NewServer(emptyPathRepo{})
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mapping.prom?apps=ghost", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Fatalf("expected empty exposition for pathless app, got %q", body)
	}
}

// countingRepo embeds fakeRepo and counts AppServiceNames calls, proving
// mappingProm issues one batched lookup per request rather than one per app.
type countingRepo struct {
	fakeRepo
	serviceNameCalls int
}

func (r *countingRepo) AppServiceNames(ctx context.Context, apps []string) (map[string]string, error) {
	r.serviceNameCalls++
	return r.fakeRepo.AppServiceNames(ctx, apps)
}

// TestMappingPromBatchesServiceNames asserts /mapping.prom issues exactly one
// AppServiceNames call per request regardless of how many apps are named,
// replacing the former per-app name lookup.
func TestMappingPromBatchesServiceNames(t *testing.T) {
	repo := &countingRepo{}
	srv := NewServer(repo)
	req := httptest.NewRequest(http.MethodGet, "/mapping.prom?apps=a1,a2,a3", nil)
	rec := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if repo.serviceNameCalls != 1 {
		t.Fatalf("AppServiceNames called %d times, want 1", repo.serviceNameCalls)
	}
}

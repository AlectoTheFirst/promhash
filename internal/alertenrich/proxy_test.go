package alertenrich

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// fakeClient returns canned rows/err regardless of input.
type fakeClient struct {
	rows []graph.ImpactRow
	err  error
}

func (f fakeClient) ImpactByInstanceIndex(_ context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	return f.rows, f.err
}
func (f fakeClient) ImpactByName(_ context.Context, _, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	return f.rows, f.err
}

// blockingClient blocks until the context is cancelled, to exercise the timeout.
type blockingClient struct{}

func (blockingClient) ImpactByInstanceIndex(ctx context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (blockingClient) ImpactByName(ctx context.Context, _, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// captureAM is a fake upstream Alertmanager that records the last forwarded body.
func captureAM(t *testing.T, last *[]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			t.Errorf("upstream path %s", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		*last = b
		w.WriteHeader(http.StatusOK)
	}))
}

func baseCfg(client ImpactClient, upstreams []string) ProxyConfig {
	return ProxyConfig{
		Client:     client,
		Upstreams:  upstreams,
		LabelMap:   LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"},
		Render:     RenderCfg{Prefix: "promhash_", EnrichLabels: true},
		Timeout:    time.Second,
		Registerer: prometheus.NewRegistry(),
		Now:        func() time.Time { return time.Unix(1700000600, 0) }, // after sample endsAt below
	}
}

const firingBatch = `[{"labels":{"alertname":"IfDown","instance":"10.0.0.1:161","ifIndex":"42"},
"annotations":{"summary":"x"},"startsAt":"2026-06-03T10:00:00Z","endsAt":"2030-01-01T00:00:00Z",
"generatorURL":"http://prom"}]`

func postAlerts(t *testing.T, p *Proxy, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", strings.NewReader(body))
	p.ServeHTTP(rec, req)
	return rec
}

func TestProxyEnrichesAndForwards(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	p := NewProxy(baseCfg(fakeClient{rows: twoRows()}, []string{am.URL}))
	rec := postAlerts(t, p, firingBatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body)
	}
	var out []map[string]any
	if err := json.Unmarshal(last, &out); err != nil {
		t.Fatalf("forwarded body not JSON: %v (%s)", err, last)
	}
	labels := out[0]["labels"].(map[string]any)
	if labels["promhash_app_count"] != "2" || labels["promhash_max_criticality"] != "critical" {
		t.Fatalf("labels not stamped: %+v", labels)
	}
	annotations := out[0]["annotations"].(map[string]any)
	if annotations["promhash_blast_radius"] != "2 apps, 1 customer" {
		t.Fatalf("annotation not stamped: %+v", annotations)
	}
	// Untouched field preserved.
	if out[0]["generatorURL"] != "http://prom" {
		t.Fatalf("generatorURL lost: %+v", out[0])
	}
}

func TestProxyFailOpenOnClientError(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	p := NewProxy(baseCfg(fakeClient{err: io.ErrUnexpectedEOF}, []string{am.URL}))
	rec := postAlerts(t, p, firingBatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(last, &out)
	if _, found := out[0]["labels"].(map[string]any)["promhash_app_count"]; found {
		t.Fatal("alert must be forwarded unchanged on client error")
	}
}

func TestProxyFailOpenOnTimeout(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	cfg := baseCfg(blockingClient{}, []string{am.URL})
	cfg.Timeout = 20 * time.Millisecond
	p := NewProxy(cfg)
	rec := postAlerts(t, p, firingBatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(last, &out)
	if _, found := out[0]["labels"].(map[string]any)["promhash_app_count"]; found {
		t.Fatal("alert must be forwarded unchanged on timeout")
	}
}

func TestProxyNoKeyPassthrough(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	p := NewProxy(baseCfg(fakeClient{rows: twoRows()}, []string{am.URL}))
	// No instance/ifIndex/ifName labels => no key => unchanged.
	rec := postAlerts(t, p, `[{"labels":{"alertname":"X"},"annotations":{}}]`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(last, &out)
	if len(out[0]["labels"].(map[string]any)) != 1 {
		t.Fatalf("labels mutated for un-correlatable alert: %+v", out[0]["labels"])
	}
}

func TestProxyAllUpstreamsFail500(t *testing.T) {
	p := NewProxy(baseCfg(fakeClient{rows: twoRows()}, []string{"http://127.0.0.1:1"})) // unroutable
	rec := postAlerts(t, p, firingBatch)
	if rec.Code < 500 {
		t.Fatalf("expected 5xx when all upstreams fail, got %d", rec.Code)
	}
}

func TestProxyBadJSON400(t *testing.T) {
	p := NewProxy(baseCfg(fakeClient{}, []string{"http://example"}))
	rec := postAlerts(t, p, `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// flakyClient serves rows until fail is set, then errors. Used to model the
// promhash-api becoming unreachable between an alert firing and resolving.
type flakyClient struct {
	rows []graph.ImpactRow
	fail bool
}

func (f *flakyClient) ImpactByInstanceIndex(_ context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	if f.fail {
		return nil, io.ErrUnexpectedEOF
	}
	return f.rows, nil
}
func (f *flakyClient) ImpactByName(_ context.Context, _, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	if f.fail {
		return nil, io.ErrUnexpectedEOF
	}
	return f.rows, nil
}

// TestProxyResolvedUsesCachedLookupWhenAPIDown: the firing alert was enriched,
// so its resolved counterpart MUST carry the same derived labels even if the
// impact lookup fails at resolve time — otherwise the fingerprints differ and
// the alert clears only via Alertmanager's resolve_timeout. The proxy caches
// successful lookups by (correlation key, startsAt) to keep that guarantee.
func TestProxyResolvedUsesCachedLookupWhenAPIDown(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	client := &flakyClient{rows: twoRows()}
	p := NewProxy(baseCfg(client, []string{am.URL}))

	// Phase 1: firing alert, lookup succeeds, labels stamped.
	rec := postAlerts(t, p, firingBatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("firing: code %d", rec.Code)
	}

	// Phase 2: the SAME alert (same labels, same startsAt) resolves while the
	// API is down.
	client.fail = true
	resolved := `[{"labels":{"alertname":"IfDown","instance":"10.0.0.1:161","ifIndex":"42"},
"annotations":{"summary":"x"},"startsAt":"2026-06-03T10:00:00Z","endsAt":"2026-06-03T10:30:00Z",
"generatorURL":"http://prom"}]`
	rec = postAlerts(t, p, resolved)
	if rec.Code != http.StatusOK {
		t.Fatalf("resolved: code %d", rec.Code)
	}

	var out []map[string]any
	if err := json.Unmarshal(last, &out); err != nil {
		t.Fatalf("forwarded body not JSON: %v (%s)", err, last)
	}
	labels := out[0]["labels"].(map[string]any)
	if labels["promhash_app_count"] != "2" {
		t.Fatalf("resolved alert lost derived labels on API failure (fingerprint mismatch): %+v", labels)
	}
}

// TestProxyNoCacheForNeverEnrichedAlert: an alert whose FIRST lookup fails has
// nothing cached and must pass through unchanged (plain fail-open).
func TestProxyNoCacheForNeverEnrichedAlert(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	client := &flakyClient{rows: twoRows(), fail: true}
	p := NewProxy(baseCfg(client, []string{am.URL}))
	rec := postAlerts(t, p, firingBatch)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(last, &out)
	if _, found := out[0]["labels"].(map[string]any)["promhash_app_count"]; found {
		t.Fatal("never-enriched alert must be forwarded unchanged on lookup failure")
	}
}

// TestProxyResolvedKeepsLabelsDropsAnnotation asserts that for a resolved alert
// with EnrichResolved=false the derived LABELS are still applied (fingerprint
// match) but the impact ANNOTATION is omitted.
func TestProxyResolvedKeepsLabelsDropsAnnotation(t *testing.T) {
	var last []byte
	am := captureAM(t, &last)
	defer am.Close()

	cfg := baseCfg(fakeClient{rows: twoRows()}, []string{am.URL})
	cfg.EnrichResolved = false
	cfg.Now = func() time.Time { return time.Unix(1700000600, 0) }
	p := NewProxy(cfg)
	// endsAt in the past relative to Now => resolved.
	resolved := `[{"labels":{"alertname":"IfDown","instance":"10.0.0.1:161","ifIndex":"42"},
"annotations":{"summary":"x"},"startsAt":"2023-11-14T00:00:00Z","endsAt":"2023-11-14T22:13:00Z"}]`
	rec := postAlerts(t, p, resolved)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var out []map[string]any
	_ = json.Unmarshal(last, &out)
	if out[0]["labels"].(map[string]any)["promhash_app_count"] != "2" {
		t.Fatal("resolved alert must still carry derived labels (fingerprint match)")
	}
	if _, found := out[0]["annotations"].(map[string]any)["promhash_blast_radius"]; found {
		t.Fatal("resolved annotation must be omitted when EnrichResolved is false")
	}
}

func TestServeHTTPRejectsOversizedBody(t *testing.T) {
	forwarded := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
	}))
	defer up.Close()

	p := NewProxy(ProxyConfig{
		Client:     fakeClient{},
		Upstreams:  []string{up.URL},
		LabelMap:   LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"},
		Render:     RenderCfg{Prefix: "promhash_"},
		Registerer: prometheus.NewRegistry(),
	})

	big := bytes.Repeat([]byte("a"), maxAlertBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(big))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if forwarded {
		t.Fatal("oversized batch must not be forwarded upstream")
	}
}

// concurrencyClient records the maximum number of in-flight lookups.
type concurrencyClient struct {
	cur, max atomic.Int64
}

func (c *concurrencyClient) observe() func() {
	n := c.cur.Add(1)
	for {
		m := c.max.Load()
		if n <= m || c.max.CompareAndSwap(m, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	return func() { c.cur.Add(-1) }
}

func (c *concurrencyClient) ImpactByInstanceIndex(_ context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	defer c.observe()()
	return []graph.ImpactRow{{App: "a", Service: "s", Owner: "o"}}, nil
}

func (c *concurrencyClient) ImpactByName(_ context.Context, _, _ string, _ time.Time) ([]graph.ImpactRow, error) {
	defer c.observe()()
	return []graph.ImpactRow{{App: "a", Service: "s", Owner: "o"}}, nil
}

func TestServeHTTPEnrichesConcurrently(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer up.Close()

	client := &concurrencyClient{}
	p := NewProxy(ProxyConfig{
		Client:     client,
		Upstreams:  []string{up.URL},
		LabelMap:   LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"},
		Render:     RenderCfg{Prefix: "promhash_"},
		Registerer: prometheus.NewRegistry(),
	})

	// 8 distinct correlatable alerts.
	var batch []map[string]any
	for i := 0; i < 8; i++ {
		batch = append(batch, map[string]any{
			"labels":   map[string]string{"alertname": "IfDown", "instance": fmt.Sprintf("10.0.0.%d:9116", i), "ifIndex": "1"},
			"startsAt": "2026-07-10T00:00:00Z",
		})
	}
	body, _ := json.Marshal(batch)
	req := httptest.NewRequest(http.MethodPost, "/api/v2/alerts", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := client.max.Load(); got < 2 {
		t.Fatalf("max concurrent lookups = %d, want >= 2 (serial enrichment)", got)
	}
}

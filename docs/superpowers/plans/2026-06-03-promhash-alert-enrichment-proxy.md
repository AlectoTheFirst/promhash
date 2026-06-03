# Alert-Enrichment Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A stateless HTTP proxy that sits between Prometheus and Alertmanager, enriching firing alerts in-flight with graph blast-radius (affected apps/services/owners/customers/criticality) before Alertmanager routes and notifies.

**Architecture:** Prometheus posts alerts to the proxy (`/api/v2/alerts`); the proxy correlates each alert's `(instance, ifIndex)` (or `(device, ifName)` fallback) to the graph via the existing promhash-api `/impact` endpoint, stamps bounded routing labels + rich annotations onto the alert, then forwards the batch to the real Alertmanager cluster. The proxy fails open: any correlation/lookup error forwards the alert unchanged. Graph access is via the C7 API only; the proxy holds no Neo4j credentials.

**Tech Stack:** Go 1.26 (module `github.com/AlectoTheFirst/promhash`), Neo4j Go driver (graph layer only), `prometheus/client_golang` (self-metrics), stdlib `net/http`. Tests: stdlib `testing` + `httptest`; graph layer uses `testutil.Neo4j` (testcontainers) behind the `integration` build tag.

**Design reference:** [docs/superpowers/specs/2026-06-03-promhash-alert-enrichment-proxy-design.md](../specs/2026-06-03-promhash-alert-enrichment-proxy-design.md)

---

## File Structure

**Create:**
- `internal/alertenrich/alert.go` — Alertmanager v2 payload parse/serialize; byte-faithful passthrough of every field except `labels`/`annotations`.
- `internal/alertenrich/correlate.go` — alert labels + config → correlation key (pure).
- `internal/alertenrich/render.go` — `[]graph.ImpactRow` → derived labels + annotations (pure).
- `internal/alertenrich/client.go` — `ImpactClient` interface + HTTP impl over the C7 API.
- `internal/alertenrich/proxy.go` — HTTP handler: receive → enrich each → forward; self-metrics.
- `internal/alertenrich/*_test.go` — one test file per source file above.
- `cmd/promhash-alert-proxy/main.go` — config (flags+env), wiring, `/metrics`, graceful shutdown.
- `cmd/promhash-alert-proxy/main_test.go` — config parsing smoke test.

**Modify:**
- `internal/graph/repo.go` — add `InterfaceImpactByInstanceIndex` + `ErrAmbiguousInterface`.
- `internal/graph/impact_by_index_test.go` — new integration test (create).
- `internal/api/server.go` — extend `Repo` interface + `/impact` handler for the exact `(instance, ifIndex)` path.
- `internal/api/server_test.go` — add the new method to `fakeRepo`; add exact-path tests.
- `README.md` — short "Alert enrichment proxy" section + Prometheus config snippet.

---

## Task 1: Graph — exact `(instance, ifIndex)` impact lookup

**Files:**
- Modify: `internal/graph/repo.go`
- Test: `internal/graph/impact_by_index_test.go` (create; `//go:build integration`)

- [ ] **Step 1: Write the failing integration test**

Create `internal/graph/impact_by_index_test.go`:

```go
//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

// TestInterfaceImpactByInstanceIndex seeds the two-candidate-path fixture
// (interface:a has instance=10.0.0.1, ifIndex=42) and asserts the exact
// (instance, ifIndex) lookup returns the same impacted app as the phash path.
func TestInterfaceImpactByInstanceIndex(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedTwoCandidatePaths(t, ctx, r)

	rows, err := r.InterfaceImpactByInstanceIndex(ctx, "10.0.0.1", 42, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || rows[0].App != "payments" {
		t.Fatalf("expected payments impact, got %+v", rows)
	}
}

// TestInterfaceImpactByInstanceIndexNoMatch asserts an unknown (instance,
// ifIndex) returns an empty slice and no error (fail-open friendly).
func TestInterfaceImpactByInstanceIndexNoMatch(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)
	seedTwoCandidatePaths(t, ctx, r)

	rows, err := r.InterfaceImpactByInstanceIndex(ctx, "10.9.9.9", 999, time.Now())
	if err != nil {
		t.Fatalf("expected nil error for no match, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty rows for no match, got %+v", rows)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestInterfaceImpactByInstanceIndex -v`
Expected: FAIL to compile — `r.InterfaceImpactByInstanceIndex undefined`.

- [ ] **Step 3: Implement the lookup**

In `internal/graph/repo.go`, add the error sentinel next to `ErrNotFound` (around line 111):

```go
// ErrAmbiguousInterface is returned when more than one Interface node shares the
// same (instance, ifIndex). The catalog should make this pair unique; if it ever
// occurs the caller treats it as no match rather than guessing.
var ErrAmbiguousInterface = fmt.Errorf("graph: multiple interfaces match instance+ifIndex")
```

Then add the method (place after `InterfaceImpact`, around line 422):

```go
// InterfaceImpactByInstanceIndex resolves the Interface by exact (instance,
// ifIndex) and returns its impact rows at time at, reusing the InterfaceImpact
// traversal so impact logic lives in one place. Zero matches → (nil, nil): the
// interface is simply not in the graph, which callers treat as "no impact".
// More than one match → ErrAmbiguousInterface (should not happen).
func (r *Repo) InterfaceImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]ImpactRow, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (n:Interface {instance:$instance, ifIndex:$ifIndex}) RETURN n.phash AS phash`,
		map[string]any{"instance": instance, "ifIndex": ifIndex},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	switch len(res.Records) {
	case 0:
		return nil, nil
	case 1:
		v, _ := res.Records[0].Get("phash")
		p, _ := v.(string)
		return r.InterfaceImpact(ctx, p, at)
	default:
		return nil, ErrAmbiguousInterface
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestInterfaceImpactByInstanceIndex -v`
Expected: PASS (both subtests). Also run `go build ./...` — Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/graph/impact_by_index_test.go
git commit -m "feat(graph): exact (instance,ifIndex) impact lookup"
```

---

## Task 2: API — exact `(instance, ifIndex)` path on `/impact`

**Files:**
- Modify: `internal/api/server.go:26-38` (Repo interface), `internal/api/server.go:160-201` (impact handler)
- Test: `internal/api/server_test.go` (add fakeRepo method + new tests)

- [ ] **Step 1: Write the failing tests**

In `internal/api/server_test.go`, first add the new method to `fakeRepo` (so the package compiles against the extended interface). Place it next to the existing `InterfaceImpact` method (around line 42):

```go
func (fakeRepo) InterfaceImpactByInstanceIndex(_ context.Context, _ string, _ int, _ time.Time) ([]graph.ImpactRow, error) {
	return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
```

Then append these tests at the end of the file:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api/ -run 'TestImpactExact' -v`
Expected: FAIL — `fakeRepo` does not implement `Repo` until the interface method is added, and the exact path returns 400 ("device and ifName are required") instead of 200.

- [ ] **Step 3: Extend the Repo interface**

In `internal/api/server.go`, add to the `Repo` interface (after `InterfaceImpact`, around line 33):

```go
	// InterfaceImpactByInstanceIndex returns the rows affected by the interface
	// identified by an exact (instance, ifIndex) pair, as of the given time.
	InterfaceImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error)
```

- [ ] **Step 4: Rewrite the impact handler to branch on params**

Replace the entire `impact` function (`internal/api/server.go:160-201`) with:

```go
// impact reports the blast radius of an interface. Two correlation modes:
//   - exact: instance + ifIndex query params (preferred; used by the alert proxy)
//   - fuzzy: device + ifName query params (the ifName may be any recognized form;
//     the catalog resolver maps it to a canonical phash server-side)
// When both are supplied, the exact pair takes precedence.
func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, ok := at(r)
	if !ok {
		http.Error(w, "invalid at parameter", http.StatusBadRequest)
		return
	}

	instance, ifIndexStr := strings.TrimSpace(q.Get("instance")), strings.TrimSpace(q.Get("ifIndex"))
	if instance != "" && ifIndexStr != "" {
		ifIndex, err := strconv.Atoi(ifIndexStr)
		if err != nil {
			http.Error(w, "ifIndex must be an integer", http.StatusBadRequest)
			return
		}
		rows, err := s.repo.InterfaceImpactByInstanceIndex(r.Context(), instance, ifIndex, t)
		if err != nil {
			if errors.Is(err, graph.ErrAmbiguousInterface) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error": "ambiguous interface reference", "instance": instance, "ifIndex": ifIndex})
				return
			}
			log.Printf("api: InterfaceImpactByInstanceIndex: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeImpact(w, instance, ifIndexStr, rows)
		return
	}

	device, ifName := strings.TrimSpace(q.Get("device")), strings.TrimSpace(q.Get("ifName"))
	if device == "" || ifName == "" {
		http.Error(w, "device and ifName (or instance and ifIndex) are required", http.StatusBadRequest)
		return
	}
	rows, _, err := s.lookupImpact(r.Context(), device, ifName, t)
	if err != nil {
		var noMatch *catalog.NoMatchError
		var ambiguous *catalog.AmbiguousError
		switch {
		case errors.As(err, &noMatch):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":       "no interface matches",
				"device":      noMatch.Device,
				"ifName":      noMatch.Ref,
				"suggestions": noMatch.Suggestions,
			})
		case errors.As(err, &ambiguous):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":   "ambiguous interface reference",
				"device":  ambiguous.Device,
				"ifName":  ambiguous.Ref,
				"matches": ambiguous.Matches,
			})
		default:
			log.Printf("api: InterfaceImpact: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
		}
		return
	}
	writeImpact(w, device, ifName, rows)
}
```

(No import changes: `strconv`, `errors`, `strings`, `log`, `json` are already imported in this file.)

- [ ] **Step 5: Run the full api test package to verify all pass**

Run: `go test ./internal/api/ -v`
Expected: PASS — new `TestImpactExact*` tests pass and every existing test (fuzzy path, guards, `at` bounds) still passes.

- [ ] **Step 6: Commit**

```bash
git add internal/api/server.go internal/api/server_test.go
git commit -m "feat(api): exact (instance,ifIndex) mode on /impact"
```

---

## Task 3: alertenrich — Alertmanager v2 payload (byte-faithful)

**Files:**
- Create: `internal/alertenrich/alert.go`
- Test: `internal/alertenrich/alert_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/alertenrich/alert_test.go`:

```go
package alertenrich

import (
	"strings"
	"testing"
)

const sampleBatch = `[
  {"labels":{"alertname":"IfDown","instance":"10.0.0.1:161","ifIndex":"42"},
   "annotations":{"summary":"iface down"},
   "startsAt":"2026-06-03T10:00:00Z","endsAt":"2026-06-03T10:05:00Z",
   "generatorURL":"http://prom/graph"}
]`

func TestParseAndMarshalRoundTrip(t *testing.T) {
	alerts, err := parseAlerts([]byte(sampleBatch))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 {
		t.Fatalf("want 1 alert, got %d", len(alerts))
	}
	lbls, err := labelsOf(alerts[0])
	if err != nil {
		t.Fatal(err)
	}
	if lbls["instance"] != "10.0.0.1:161" || lbls["ifIndex"] != "42" {
		t.Fatalf("labels: %+v", lbls)
	}
	if got := startsAtOf(alerts[0]); got != "2026-06-03T10:00:00Z" {
		t.Fatalf("startsAt: %q", got)
	}

	out, err := marshalAlerts(alerts)
	if err != nil {
		t.Fatal(err)
	}
	// Untouched fields must survive verbatim.
	for _, want := range []string{`"generatorURL":"http://prom/graph"`, `"endsAt":"2026-06-03T10:05:00Z"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("round-trip dropped %s; got %s", want, out)
		}
	}
}

func TestSetLabelsAndAnnotations(t *testing.T) {
	alerts, _ := parseAlerts([]byte(sampleBatch))
	if err := setLabels(alerts[0], map[string]string{"alertname": "IfDown", "promhash_app_count": "3"}); err != nil {
		t.Fatal(err)
	}
	if err := setAnnotations(alerts[0], map[string]string{"summary": "iface down", "promhash_blast_radius": "3 apps"}); err != nil {
		t.Fatal(err)
	}
	out, _ := marshalAlerts(alerts)
	for _, want := range []string{`"promhash_app_count":"3"`, `"promhash_blast_radius":"3 apps"`, `"generatorURL":"http://prom/graph"`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
}

func TestEndsAtResolved(t *testing.T) {
	alerts, _ := parseAlerts([]byte(sampleBatch))
	if got := endsAtOf(alerts[0]); got != "2026-06-03T10:05:00Z" {
		t.Fatalf("endsAt: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alertenrich/ -run 'TestParse|TestSet|TestEndsAt' -v`
Expected: FAIL — package/functions don't exist.

- [ ] **Step 3: Implement the payload helpers**

Create `internal/alertenrich/alert.go`:

```go
// Package alertenrich enriches Alertmanager v2 alerts in flight with graph
// blast-radius data. It parses the alert batch, correlates each alert to an
// interface, looks up impact, and stamps bounded labels + rich annotations,
// forwarding the result to the upstream Alertmanager. It fails open: any error
// leaves the alert unchanged.
package alertenrich

import "encoding/json"

// rawAlert holds every field of one Alertmanager v2 alert as raw JSON so that
// fields the proxy does not touch (startsAt, endsAt, generatorURL, and any
// future fields) round-trip byte-for-byte. Only "labels" and "annotations" are
// ever decoded and re-encoded.
type rawAlert map[string]json.RawMessage

// parseAlerts decodes the POST /api/v2/alerts body (a JSON array) into rawAlerts.
func parseAlerts(body []byte) ([]rawAlert, error) {
	var out []rawAlert
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// marshalAlerts re-encodes the batch. Fields untouched by the proxy are emitted
// from their stored raw JSON unchanged.
func marshalAlerts(as []rawAlert) ([]byte, error) { return json.Marshal(as) }

// decodeStringMap decodes the named object field into a map[string]string.
// A missing field yields a non-nil empty map so callers can always add keys.
func decodeStringMap(a rawAlert, field string) (map[string]string, error) {
	m := map[string]string{}
	raw, ok := a[field]
	if !ok || len(raw) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func labelsOf(a rawAlert) (map[string]string, error)      { return decodeStringMap(a, "labels") }
func annotationsOf(a rawAlert) (map[string]string, error) { return decodeStringMap(a, "annotations") }

// encodeStringMap re-encodes m into the named object field.
func encodeStringMap(a rawAlert, field string, m map[string]string) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	a[field] = raw
	return nil
}

func setLabels(a rawAlert, m map[string]string) error      { return encodeStringMap(a, "labels", m) }
func setAnnotations(a rawAlert, m map[string]string) error { return encodeStringMap(a, "annotations", m) }

// decodeString decodes the named string field; returns "" when absent.
func decodeString(a rawAlert, field string) string {
	raw, ok := a[field]
	if !ok || len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

func startsAtOf(a rawAlert) string { return decodeString(a, "startsAt") }
func endsAtOf(a rawAlert) string   { return decodeString(a, "endsAt") }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/alertenrich/ -run 'TestParse|TestSet|TestEndsAt' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/alertenrich/alert.go internal/alertenrich/alert_test.go
git commit -m "feat(alertenrich): byte-faithful Alertmanager v2 payload helpers"
```

---

## Task 4: alertenrich — correlation key

**Files:**
- Create: `internal/alertenrich/correlate.go`
- Test: `internal/alertenrich/correlate_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/alertenrich/correlate_test.go`:

```go
package alertenrich

import "testing"

func defaultMap() LabelMap {
	return LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"}
}

func TestCorrelateExact(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "10.0.0.1:161", "ifIndex": "42"}, defaultMap())
	if !ok || k.Kind != KeyExact || k.Instance != "10.0.0.1:161" || k.IfIndex != 42 {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}

func TestCorrelateNameFallback(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "rtr-core-1", "ifName": "Te0/1/2"}, defaultMap())
	if !ok || k.Kind != KeyName || k.Device != "rtr-core-1" || k.IfName != "Te0/1/2" {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}

func TestCorrelateNoDevice(t *testing.T) {
	if _, ok := Correlate(map[string]string{"ifIndex": "42"}, defaultMap()); ok {
		t.Fatal("expected no key when device label absent")
	}
}

func TestCorrelateNoIfaceLabels(t *testing.T) {
	if _, ok := Correlate(map[string]string{"instance": "10.0.0.1"}, defaultMap()); ok {
		t.Fatal("expected no key when neither ifIndex nor ifName present")
	}
}

func TestCorrelateNonIntIfIndexFallsToName(t *testing.T) {
	k, ok := Correlate(map[string]string{"instance": "rtr-core-1", "ifIndex": "n/a", "ifName": "Te0/1/2"}, defaultMap())
	if !ok || k.Kind != KeyName {
		t.Fatalf("expected name fallback when ifIndex non-integer, got %+v ok=%v", k, ok)
	}
}

func TestCorrelateCustomLabels(t *testing.T) {
	m := LabelMap{DeviceLabel: "host", IfIndexLabel: "idx", IfNameLabel: "iface"}
	k, ok := Correlate(map[string]string{"host": "10.0.0.2", "idx": "7"}, m)
	if !ok || k.Kind != KeyExact || k.Instance != "10.0.0.2" || k.IfIndex != 7 {
		t.Fatalf("got %+v ok=%v", k, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestCorrelate -v`
Expected: FAIL — `Correlate`, `LabelMap`, `KeyExact` undefined.

- [ ] **Step 3: Implement correlation**

Create `internal/alertenrich/correlate.go`:

```go
package alertenrich

import "strconv"

// LabelMap names the alert labels that carry the device/interface identity.
// Defaults: DeviceLabel "instance", IfIndexLabel "ifIndex", IfNameLabel "ifName".
type LabelMap struct {
	DeviceLabel  string
	IfIndexLabel string
	IfNameLabel  string
}

// KeyKind distinguishes the two correlation strategies.
type KeyKind int

const (
	KeyNone  KeyKind = iota // no usable correlation labels present
	KeyExact                // match Interface by (instance, ifIndex)
	KeyName                 // match Interface by (device-name, ifName) via the catalog resolver
)

// Key is a resolved correlation target for one alert.
type Key struct {
	Kind     KeyKind
	Instance string // KeyExact: value of the device label (Prometheus instance)
	IfIndex  int    // KeyExact
	Device   string // KeyName: value of the device label (device name)
	IfName   string // KeyName
}

// Correlate derives a correlation Key from an alert's labels using m. The device
// label is required. When it and an integer ifIndex label are present, an exact
// key is returned; otherwise, when a name label is present, a name key is
// returned. Returns ok=false when no usable key can be built (caller forwards
// the alert un-enriched).
func Correlate(labels map[string]string, m LabelMap) (Key, bool) {
	dev := labels[m.DeviceLabel]
	if dev == "" {
		return Key{}, false
	}
	if s := labels[m.IfIndexLabel]; s != "" {
		if idx, err := strconv.Atoi(s); err == nil {
			return Key{Kind: KeyExact, Instance: dev, IfIndex: idx}, true
		}
	}
	if name := labels[m.IfNameLabel]; name != "" {
		return Key{Kind: KeyName, Device: dev, IfName: name}, true
	}
	return Key{}, false
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/alertenrich/ -run TestCorrelate -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/alertenrich/correlate.go internal/alertenrich/correlate_test.go
git commit -m "feat(alertenrich): config-driven correlation key"
```

---

## Task 5: alertenrich — render labels + annotations

**Files:**
- Create: `internal/alertenrich/render.go`
- Test: `internal/alertenrich/render_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/alertenrich/render_test.go`:

```go
package alertenrich

import (
	"reflect"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func twoRows() []graph.ImpactRow {
	return []graph.ImpactRow{
		{App: "payments", Service: "payments-api", Owner: "team-payments", Customer: "acme", Criticality: "critical"},
		{App: "ledger", Service: "ledger-api", Owner: "team-ledger"},
	}
}

func TestRenderLabelsAndAnnotations(t *testing.T) {
	labels, annotations := Render(twoRows(), RenderCfg{Prefix: "promhash_", EnrichLabels: true})

	wantLabels := map[string]string{
		"promhash_max_criticality": "critical",
		"promhash_app_count":       "2",
		"promhash_customer_impact": "true",
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels:\n got %+v\nwant %+v", labels, wantLabels)
	}

	wantImpact := "apps affected (2):\n" +
		"- ledger (ledger-api) owner team-ledger\n" +
		"- payments (payments-api) owner team-payments customer acme [critical]"
	if annotations["promhash_impact"] != wantImpact {
		t.Fatalf("promhash_impact:\n got:\n%s\nwant:\n%s", annotations["promhash_impact"], wantImpact)
	}
	if annotations["promhash_blast_radius"] != "2 apps, 1 customer" {
		t.Fatalf("blast_radius: %q", annotations["promhash_blast_radius"])
	}
}

func TestRenderEmptyRows(t *testing.T) {
	labels, annotations := Render(nil, RenderCfg{Prefix: "promhash_", EnrichLabels: true})
	if labels != nil || annotations != nil {
		t.Fatalf("expected nil maps for empty impact, got labels=%+v annotations=%+v", labels, annotations)
	}
}

func TestRenderLabelsDisabled(t *testing.T) {
	labels, annotations := Render(twoRows(), RenderCfg{Prefix: "promhash_", EnrichLabels: false})
	if labels != nil {
		t.Fatalf("expected nil labels when EnrichLabels false, got %+v", labels)
	}
	if annotations["promhash_blast_radius"] == "" {
		t.Fatal("annotations should still be produced when labels disabled")
	}
}

func TestRenderNoCriticalityNoCustomer(t *testing.T) {
	rows := []graph.ImpactRow{{App: "edge", Service: "edge-svc", Owner: "team-edge"}}
	labels, annotations := Render(rows, RenderCfg{Prefix: "promhash_", EnrichLabels: true})
	if labels["promhash_max_criticality"] != "unknown" {
		t.Fatalf("max_criticality: %q (want unknown)", labels["promhash_max_criticality"])
	}
	if labels["promhash_customer_impact"] != "false" {
		t.Fatalf("customer_impact: %q (want false)", labels["promhash_customer_impact"])
	}
	if annotations["promhash_blast_radius"] != "1 app, 0 customers" {
		t.Fatalf("blast_radius: %q", annotations["promhash_blast_radius"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestRender -v`
Expected: FAIL — `Render`, `RenderCfg` undefined.

- [ ] **Step 3: Implement rendering**

Create `internal/alertenrich/render.go`:

```go
package alertenrich

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// RenderCfg controls how impact rows become alert attachments.
type RenderCfg struct {
	Prefix       string // key prefix, e.g. "promhash_"
	EnrichLabels bool   // when false, no labels are produced (annotations only)
}

// critRank orders criticality strings; higher wins. Unknown/empty rank as 0.
var critRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}

// Render turns impact rows into the labels and annotations to attach to an alert.
// Empty rows produce (nil, nil) so the alert passes through unchanged. When
// EnrichLabels is false, the labels map is nil but annotations are still built.
// Output is deterministic: rows are sorted by (app, customer) before rendering.
func Render(rows []graph.ImpactRow, cfg RenderCfg) (labels, annotations map[string]string) {
	if len(rows) == 0 {
		return nil, nil
	}

	sorted := append([]graph.ImpactRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].App != sorted[j].App {
			return sorted[i].App < sorted[j].App
		}
		return sorted[i].Customer < sorted[j].Customer
	})

	apps := map[string]struct{}{}
	customers := map[string]struct{}{}
	maxRank, maxCrit := 0, "unknown"
	var lines []string
	for _, r := range sorted {
		apps[r.App] = struct{}{}
		if r.Customer != "" {
			customers[r.Customer] = struct{}{}
		}
		if rk := critRank[r.Criticality]; rk > maxRank {
			maxRank, maxCrit = rk, r.Criticality
		}
		line := fmt.Sprintf("- %s (%s) owner %s", r.App, r.Service, r.Owner)
		if r.Customer != "" {
			line += " customer " + r.Customer
		}
		if r.Criticality != "" {
			line += " [" + r.Criticality + "]"
		}
		lines = append(lines, line)
	}

	annotations = map[string]string{
		cfg.Prefix + "impact": fmt.Sprintf("apps affected (%d):\n%s", len(apps), strings.Join(lines, "\n")),
		cfg.Prefix + "blast_radius": fmt.Sprintf("%s, %s",
			plural(len(apps), "app"), plural(len(customers), "customer")),
	}

	if cfg.EnrichLabels {
		labels = map[string]string{
			cfg.Prefix + "max_criticality": maxCrit,
			cfg.Prefix + "app_count":       strconv.Itoa(len(apps)),
			cfg.Prefix + "customer_impact": strconv.FormatBool(len(customers) > 0),
		}
	}
	return labels, annotations
}

// plural renders "1 app" / "2 apps".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/alertenrich/ -run TestRender -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/alertenrich/render.go internal/alertenrich/render_test.go
git commit -m "feat(alertenrich): render bounded labels + impact annotations"
```

---

## Task 6: alertenrich — ImpactClient over the C7 API

**Files:**
- Create: `internal/alertenrich/client.go`
- Test: `internal/alertenrich/client_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/alertenrich/client_test.go`:

```go
package alertenrich

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAPIClientExact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/impact" {
			t.Errorf("path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("instance") != "10.0.0.1:161" || q.Get("ifIndex") != "42" {
			t.Errorf("query %v", q)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"interface":"10.0.0.1:161/42","impact":[{"app":"payments","service":"payments-api","owner":"team-payments"}]}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client())
	rows, err := c.ImpactByInstanceIndex(context.Background(), "10.0.0.1:161", 42, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].App != "payments" {
		t.Fatalf("rows %+v", rows)
	}
}

func TestAPIClientNotFoundIsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"no interface matches"}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client())
	rows, err := c.ImpactByName(context.Background(), "rtr-x", "Te9", time.Time{})
	if err != nil {
		t.Fatalf("404 must map to empty rows + nil error, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected empty rows, got %+v", rows)
	}
}

func TestAPIClientServerErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client())
	if _, err := c.ImpactByInstanceIndex(context.Background(), "10.0.0.1", 1, time.Time{}); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestAPIClientAtParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("at") != "1700000000" {
			t.Errorf("at=%q", r.URL.Query().Get("at"))
		}
		_, _ = w.Write([]byte(`{"impact":[]}`))
	}))
	defer srv.Close()

	c := NewAPIClient(srv.URL, srv.Client())
	_, _ = c.ImpactByInstanceIndex(context.Background(), "10.0.0.1", 1, time.Unix(1700000000, 0))
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestAPIClient -v`
Expected: FAIL — `NewAPIClient` undefined.

- [ ] **Step 3: Implement the client**

Create `internal/alertenrich/client.go`:

```go
package alertenrich

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// ImpactClient is the proxy's read-only view of the graph. Implementations must
// map "interface not found" (and ambiguous) to (nil, nil) — no impact — so the
// proxy fails open; only genuine transport/server errors return a non-nil error.
type ImpactClient interface {
	ImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error)
	ImpactByName(ctx context.Context, device, ifName string, at time.Time) ([]graph.ImpactRow, error)
}

// apiClient calls the promhash-api C7 /impact endpoint.
type apiClient struct {
	base string
	hc   *http.Client
}

// NewAPIClient returns an ImpactClient backed by the C7 API at base (e.g.
// "http://promhash-api:8080"). hc may be nil, in which case http.DefaultClient
// is used (callers should pass a client with a sane timeout in production).
func NewAPIClient(base string, hc *http.Client) ImpactClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &apiClient{base: base, hc: hc}
}

func (c *apiClient) ImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error) {
	q := url.Values{"instance": {instance}, "ifIndex": {strconv.Itoa(ifIndex)}}
	return c.query(ctx, q, at)
}

func (c *apiClient) ImpactByName(ctx context.Context, device, ifName string, at time.Time) ([]graph.ImpactRow, error) {
	q := url.Values{"device": {device}, "ifName": {ifName}}
	return c.query(ctx, q, at)
}

// query issues GET /impact with q (+ optional at) and decodes the wrapped body.
// 404/409 (no-match / ambiguous) map to empty rows so an un-mappable interface
// fails open. Any other non-2xx is an error.
func (c *apiClient) query(ctx context.Context, q url.Values, at time.Time) ([]graph.ImpactRow, error) {
	if !at.IsZero() {
		q.Set("at", strconv.FormatInt(at.Unix(), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/impact?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var body struct {
			Impact []graph.ImpactRow `json:"impact"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		return body.Impact, nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil // fail open: treat as no impact
	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("promhash-api /impact: status %d", resp.StatusCode)
	}
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/alertenrich/ -run TestAPIClient -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/alertenrich/client.go internal/alertenrich/client_test.go
git commit -m "feat(alertenrich): C7 API impact client (fail-open on 404/409)"
```

---

## Task 7: alertenrich — proxy handler + metrics

**Files:**
- Create: `internal/alertenrich/proxy.go`
- Test: `internal/alertenrich/proxy_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/alertenrich/proxy_test.go`:

```go
package alertenrich

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
		Client:    client,
		Upstreams: upstreams,
		LabelMap:  LabelMap{DeviceLabel: "instance", IfIndexLabel: "ifIndex", IfNameLabel: "ifName"},
		Render:    RenderCfg{Prefix: "promhash_", EnrichLabels: true},
		Timeout:   time.Second,
		Registerer: prometheus.NewRegistry(),
		Now:       func() time.Time { return time.Unix(1700000600, 0) }, // after sample endsAt below
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestProxy -v`
Expected: FAIL — `NewProxy`, `Proxy`, `ProxyConfig` undefined.

- [ ] **Step 3: Implement the proxy + metrics**

Create `internal/alertenrich/proxy.go`:

```go
package alertenrich

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// ProxyConfig configures a Proxy. Client, Upstreams, LabelMap and Render are
// required; the rest have sane zero-value fallbacks applied by NewProxy.
type ProxyConfig struct {
	Client         ImpactClient
	Upstreams      []string             // upstream Alertmanager base URLs
	LabelMap       LabelMap             // which alert labels carry device/iface identity
	Render         RenderCfg            // how impact becomes labels/annotations
	Timeout        time.Duration        // per-alert lookup budget (default 2s)
	EnrichResolved bool                 // also add impact annotation to resolved alerts
	HTTPClient     *http.Client         // forwarding client (default 10s timeout)
	Registerer     prometheus.Registerer // self-metrics target (default DefaultRegisterer)
	Now            func() time.Time     // clock for resolved detection (default time.Now)
}

// Proxy is the HTTP handler that enriches an Alertmanager v2 alert batch and
// forwards it upstream. It is stateless and safe for concurrent use.
type Proxy struct {
	client         ImpactClient
	upstreams      []string
	labelMap       LabelMap
	render         RenderCfg
	timeout        time.Duration
	enrichResolved bool
	hc             *http.Client
	now            func() time.Time
	m              *metrics
}

// errAllUpstreams indicates no upstream Alertmanager accepted the batch.
var errAllUpstreams = errors.New("alertenrich: all upstream alertmanagers failed")

// NewProxy builds a Proxy from cfg, applying defaults and registering metrics.
func NewProxy(cfg ProxyConfig) *Proxy {
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Registerer == nil {
		cfg.Registerer = prometheus.DefaultRegisterer
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Proxy{
		client:         cfg.Client,
		upstreams:      cfg.Upstreams,
		labelMap:       cfg.LabelMap,
		render:         cfg.Render,
		timeout:        cfg.Timeout,
		enrichResolved: cfg.EnrichResolved,
		hc:             cfg.HTTPClient,
		now:            cfg.Now,
		m:              newMetrics(cfg.Registerer),
	}
}

// ServeHTTP handles POST /api/v2/alerts: parse, enrich each alert (fail-open),
// then forward the batch to all upstream Alertmanagers.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	alerts, err := parseAlerts(body)
	if err != nil {
		http.Error(w, "invalid alert payload", http.StatusBadRequest)
		return
	}
	p.m.received.Add(float64(len(alerts)))

	for _, a := range alerts {
		p.enrichOne(r.Context(), a)
	}

	out, err := marshalAlerts(alerts)
	if err != nil {
		// Should not happen; fall back to forwarding the original bytes.
		out = body
	}
	if err := p.forward(r.Context(), out); err != nil {
		log.Printf("alertenrich: forward: %v", err)
		http.Error(w, "upstream alertmanager unavailable", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// enrichOne mutates a in place when impact is found; on any failure it leaves a
// unchanged and records the reason. Never returns an error (fail-open).
func (p *Proxy) enrichOne(ctx context.Context, a rawAlert) {
	labels, err := labelsOf(a)
	if err != nil {
		p.m.passthrough.WithLabelValues("error").Inc()
		return
	}
	key, ok := Correlate(labels, p.labelMap)
	if !ok {
		p.m.passthrough.WithLabelValues("no_key").Inc()
		return
	}

	var at time.Time
	if s := startsAtOf(a); s != "" {
		if ts, perr := time.Parse(time.RFC3339, s); perr == nil {
			at = ts
		}
	}

	lctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := p.now()
	rows, lerr := p.lookup(lctx, key, at)
	p.m.lookup.Observe(p.now().Sub(start).Seconds())
	if lerr != nil {
		reason := "error"
		if errors.Is(lerr, context.DeadlineExceeded) {
			reason = "timeout"
		}
		p.m.passthrough.WithLabelValues(reason).Inc()
		return
	}
	if len(rows) == 0 {
		p.m.passthrough.WithLabelValues("no_match").Inc()
		return
	}

	outLabels, outAnnotations := Render(rows, p.render)

	// Labels are applied to BOTH firing and resolved alerts so the resolved
	// alert keeps the same fingerprint and clears correctly (design §8).
	if outLabels != nil {
		for k, v := range outLabels {
			labels[k] = v
		}
		if err := setLabels(a, labels); err != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
	}

	// The impact annotation is added to firing alerts always, and to resolved
	// alerts only when EnrichResolved is set.
	if outAnnotations != nil && (!p.isResolved(a) || p.enrichResolved) {
		annotations, aerr := annotationsOf(a)
		if aerr != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
		for k, v := range outAnnotations {
			annotations[k] = v
		}
		if err := setAnnotations(a, annotations); err != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
	}

	p.m.enriched.Inc()
}

// lookup dispatches to the exact or name client method based on the key kind.
func (p *Proxy) lookup(ctx context.Context, key Key, at time.Time) ([]graph.ImpactRow, error) {
	switch key.Kind {
	case KeyExact:
		return p.client.ImpactByInstanceIndex(ctx, key.Instance, key.IfIndex, at)
	case KeyName:
		return p.client.ImpactByName(ctx, key.Device, key.IfName, at)
	default:
		return nil, nil
	}
}

// isResolved reports whether the alert's endsAt is in the past relative to the
// proxy clock (Alertmanager v2 marks an alert resolved by an elapsed endsAt).
func (p *Proxy) isResolved(a rawAlert) bool {
	s := endsAtOf(a)
	if s == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	return ts.Before(p.now())
}

// forward POSTs body to every upstream Alertmanager's /api/v2/alerts. It returns
// nil if at least one upstream accepts (2xx); errAllUpstreams otherwise.
func (p *Proxy) forward(ctx context.Context, body []byte) error {
	var ok bool
	for _, base := range p.upstreams {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/alerts", bytes.NewReader(body))
		if err != nil {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.hc.Do(req)
		if err != nil {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		ok = true
	}
	if !ok {
		return errAllUpstreams
	}
	return nil
}
```

- [ ] **Step 4: Implement the metrics helper**

Append to `internal/alertenrich/proxy.go`:

```go
// metrics holds the proxy's self-observability counters/histogram.
type metrics struct {
	received    prometheus.Counter
	enriched    prometheus.Counter
	passthrough *prometheus.CounterVec
	forwardErr  *prometheus.CounterVec
	lookup      prometheus.Histogram
}

// newMetrics constructs and registers the metric set on reg. Using a fresh
// prometheus.NewRegistry() in tests avoids duplicate-registration panics.
func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		received: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_received_total", Help: "Alerts received from Prometheus."}),
		enriched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_enriched_total", Help: "Alerts enriched with impact."}),
		passthrough: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_passthrough_total", Help: "Alerts forwarded un-enriched, by reason."},
			[]string{"reason"}),
		forwardErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_forward_errors_total", Help: "Upstream forward failures, by upstream."},
			[]string{"upstream"}),
		lookup: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "promhash_alert_proxy_lookup_seconds", Help: "Impact lookup latency.",
			Buckets: prometheus.DefBuckets}),
	}
	reg.MustRegister(m.received, m.enriched, m.passthrough, m.forwardErr, m.lookup)
	return m
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/alertenrich/ -v`
Expected: PASS — every `TestProxy*` plus the earlier alertenrich tests.

- [ ] **Step 6: Commit**

```bash
git add internal/alertenrich/proxy.go internal/alertenrich/proxy_test.go
git commit -m "feat(alertenrich): fail-open enrichment proxy handler + self-metrics"
```

---

## Task 8: cmd/promhash-alert-proxy — wiring

**Files:**
- Create: `cmd/promhash-alert-proxy/main.go`
- Test: `cmd/promhash-alert-proxy/main_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/promhash-alert-proxy/main_test.go`:

```go
package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseUpstreams(t *testing.T) {
	got := parseUpstreams("http://am-0:9093, http://am-1:9093 ,")
	want := []string{"http://am-0:9093", "http://am-1:9093"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseUpstreamsEmpty(t *testing.T) {
	if got := parseUpstreams("   "); len(got) != 0 {
		t.Fatalf("expected empty, got %v", got)
	}
}

func TestBuildConfigDefaults(t *testing.T) {
	c := buildConfig(opts{
		upstreams: "http://am:9093",
		apiBase:   "http://api:8080",
		timeout:   3 * time.Second,
	})
	if c.LabelMap.DeviceLabel != "instance" || c.LabelMap.IfIndexLabel != "ifIndex" || c.LabelMap.IfNameLabel != "ifName" {
		t.Fatalf("label map defaults wrong: %+v", c.LabelMap)
	}
	if len(c.Upstreams) != 1 || c.Upstreams[0] != "http://am:9093" {
		t.Fatalf("upstreams: %+v", c.Upstreams)
	}
	if c.Timeout != 3*time.Second {
		t.Fatalf("timeout: %v", c.Timeout)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/promhash-alert-proxy/ -v`
Expected: FAIL — `parseUpstreams`, `buildConfig`, `opts` undefined.

- [ ] **Step 3: Implement main + config**

Create `cmd/promhash-alert-proxy/main.go`:

```go
// Command promhash-alert-proxy enriches Alertmanager v2 alerts in flight with
// graph blast-radius data and forwards them to the real Alertmanager. Point
// Prometheus' alertmanagers config at this proxy. It fails open: any enrichment
// error forwards the alert unchanged.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AlectoTheFirst/promhash/internal/alertenrich"
)

// opts holds the raw flag values before assembly into a ProxyConfig.
type opts struct {
	listen         string
	upstreams      string
	apiBase        string
	deviceLabel    string
	ifIndexLabel   string
	ifNameLabel    string
	timeout        time.Duration
	prefix         string
	enrichLabels   bool
	enrichResolved bool
}

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-alert-proxy: %v", err)
		os.Exit(1)
	}
}

// parseUpstreams splits a comma-separated list into trimmed, non-empty URLs.
func parseUpstreams(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildConfig assembles a ProxyConfig from opts, wiring the C7 API client with a
// lookup-bounded HTTP client and applying default label names.
func buildConfig(o opts) alertenrich.ProxyConfig {
	if o.deviceLabel == "" {
		o.deviceLabel = "instance"
	}
	if o.ifIndexLabel == "" {
		o.ifIndexLabel = "ifIndex"
	}
	if o.ifNameLabel == "" {
		o.ifNameLabel = "ifName"
	}
	if o.prefix == "" {
		o.prefix = "promhash_"
	}
	if o.timeout == 0 {
		o.timeout = 2 * time.Second
	}
	apiHTTP := &http.Client{Timeout: o.timeout}
	return alertenrich.ProxyConfig{
		Client:         alertenrich.NewAPIClient(o.apiBase, apiHTTP),
		Upstreams:      parseUpstreams(o.upstreams),
		LabelMap:       alertenrich.LabelMap{DeviceLabel: o.deviceLabel, IfIndexLabel: o.ifIndexLabel, IfNameLabel: o.ifNameLabel},
		Render:         alertenrich.RenderCfg{Prefix: o.prefix, EnrichLabels: o.enrichLabels},
		Timeout:        o.timeout,
		EnrichResolved: o.enrichResolved,
		Registerer:     prometheus.DefaultRegisterer,
	}
}

func run() error {
	var o opts
	flag.StringVar(&o.listen, "listen", "127.0.0.1:9094", "alert intake + /metrics listen address")
	flag.StringVar(&o.upstreams, "upstreams", "", "comma-separated upstream Alertmanager base URLs")
	flag.StringVar(&o.apiBase, "promhash-api", "http://127.0.0.1:8080", "promhash-api base URL")
	flag.StringVar(&o.deviceLabel, "device-label", "instance", "alert label carrying the device/target")
	flag.StringVar(&o.ifIndexLabel, "ifindex-label", "ifIndex", "alert label carrying ifIndex")
	flag.StringVar(&o.ifNameLabel, "ifname-label", "ifName", "alert label for the name fallback")
	flag.DurationVar(&o.timeout, "lookup-timeout", 2*time.Second, "per-alert impact lookup timeout")
	flag.StringVar(&o.prefix, "label-prefix", "promhash_", "prefix for stamped labels/annotations")
	flag.BoolVar(&o.enrichLabels, "enrich-labels", true, "stamp bounded routing labels (false => annotations only)")
	flag.BoolVar(&o.enrichResolved, "enrich-resolved", true, "add impact annotation to resolved alerts too")
	flag.Parse()

	if len(parseUpstreams(o.upstreams)) == 0 {
		return errors.New("at least one -upstreams Alertmanager URL is required")
	}

	proxy := alertenrich.NewProxy(buildConfig(o))

	mux := http.NewServeMux()
	mux.Handle("POST /api/v2/alerts", proxy)
	mux.Handle("GET /metrics", promhttp.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              o.listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("promhash-alert-proxy listening on %s -> %v", o.listen, parseUpstreams(o.upstreams))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Print("promhash-alert-proxy shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
```

- [ ] **Step 4: Run the test + build to verify pass**

Run: `go test ./cmd/promhash-alert-proxy/ -v`
Expected: PASS.
Run: `go build ./...`
Expected: success (binary compiles).

- [ ] **Step 5: Commit**

```bash
git add cmd/promhash-alert-proxy/main.go cmd/promhash-alert-proxy/main_test.go
git commit -m "feat(alert-proxy): promhash-alert-proxy command wiring + /metrics"
```

---

## Task 9: Docs — README section + Prometheus config

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Add the proxy section**

Append a section to `README.md` (place it after the existing enrichment/API sections; match the surrounding heading style):

````markdown
## Alert enrichment proxy

`promhash-alert-proxy` sits between Prometheus and Alertmanager. It enriches
firing alerts in flight with graph blast-radius — affected apps, services,
owners, customers, and a max-criticality — pulled live from the graph via the
`promhash-api` `/impact` endpoint. It **fails open**: any lookup error forwards
the alert unchanged, so it never drops alerts.

It correlates each alert by an exact `(instance, ifIndex)` match (configurable
labels), falling back to a fuzzy `(device, ifName)` resolve.

Run it:

```bash
promhash-alert-proxy \
  -listen :9094 \
  -upstreams http://alertmanager-0:9093,http://alertmanager-1:9093 \
  -promhash-api http://promhash-api:8080
```

Point Prometheus at the proxy instead of Alertmanager:

```yaml
alerting:
  alertmanagers:
    - static_configs:
        - targets: ['promhash-alert-proxy:9094']
```

Enrichment appears as alert **labels** (`promhash_max_criticality`,
`promhash_app_count`, `promhash_customer_impact` — usable for Alertmanager
routing) and **annotations** (`promhash_impact`, `promhash_blast_radius` — the
full list, shown in notifications). Set `-enrich-labels=false` for
annotations-only (fingerprint-safe) operation. The proxy exposes its own metrics
at `/metrics`.
````

- [ ] **Step 2: Verify the full suite + vet**

Run: `make test && make lint`
Expected: all unit tests pass (`go test ./...`) and `go vet ./...` is clean.
Run (if a Neo4j/Docker environment is available): `make test-int`
Expected: graph integration tests including `TestInterfaceImpactByInstanceIndex` pass.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document promhash-alert-proxy usage and wiring"
```

---

## Self-Review

**Spec coverage:**
- §2.1 inline push proxy → Task 7 (`ServeHTTP` + `forward`), Task 8 (Prometheus points at it). ✓
- §2.2 annotations + bounded labels → Task 5 (`Render`). ✓
- §2.3 configurable correlation, default exact + name fallback → Task 4 (`Correlate`), Task 1/2 (exact lookup + endpoint). ✓
- §2.4 graph via C7 API → Task 6 (`apiClient`), no Neo4j import in proxy. ✓
- §5 component split → Tasks 3–8 map 1:1 to the files. ✓
- §6 correlation order + `at` = startsAt → Task 7 `enrichOne`. ✓
- §8 fail-open, fingerprint coupling (labels on resolved), HA fan-out, metrics → Task 7 (`enrichOne`/`forward`/`metrics`), tested by `TestProxyResolvedKeepsLabelsDropsAnnotation`, `TestProxyAllUpstreamsFail500`, fail-open tests. ✓
- §9 config flags → Task 8. ✓
- §10 failure table → Tasks 6/7 (404/409→empty, timeout→passthrough, all-upstreams→5xx, bad JSON→400) all have tests. ✓
- §11 testing strategy → each task is TDD; payload round-trip, correlate table, render cases, client fail-open, proxy httptest, api contract, graph integration all present. ✓

**Placeholder scan:** Clean. The proxy `lookup` returns `[]graph.ImpactRow` with the `graph` import in the block; the command imports `flag` in its block. No `TODO`/`TBD`/"handle errors appropriately"/"similar to Task N" placeholders; every code step shows complete, compilable code.

**Type consistency:** `InterfaceImpactByInstanceIndex(ctx, string, int, time.Time) ([]graph.ImpactRow, error)` is identical across graph (Task 1), the api `Repo` interface and `fakeRepo` (Task 2). `ImpactClient` method names match between `client.go` (Task 6) and `proxy.go`/`proxy_test.go` (Task 7). `RenderCfg{Prefix, EnrichLabels}`, `LabelMap{DeviceLabel, IfIndexLabel, IfNameLabel}`, `Key{Kind, Instance, IfIndex, Device, IfName}`, and `ProxyConfig` fields are used consistently across producer and consumer tasks. Label/annotation key names (`promhash_max_criticality`, `promhash_app_count`, `promhash_customer_impact`, `promhash_impact`, `promhash_blast_radius`) match between `render.go` and every assertion in `render_test.go`/`proxy_test.go`.

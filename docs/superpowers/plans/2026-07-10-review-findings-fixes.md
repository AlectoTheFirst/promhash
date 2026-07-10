# Review Findings Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the correctness, robustness, and performance findings from the 2026-07-10 whole-program review: validity-timestamp guards in the graph layer, the catalog device-name fallback, alert-proxy request limits and concurrency, batched Neo4j writes, a batched service-name lookup for the mapping exposition, dead-code removal, and a `-neo4j-db` flag.

**Architecture:** All changes are local hardening/optimization of existing components — no new services, no schema changes. Graph-layer fixes clamp/extend the validity-window invariants inside the existing managed transactions. The catalog sync is refactored into a pure build step (unit-testable) plus one batched UNWIND write. The alert proxy gains a body-size cap and bounded parallel enrichment.

**Tech Stack:** Go 1.26, Neo4j 5 (neo4j-go-driver/v5), testcontainers for integration tests, Prometheus client_golang.

## Global Constraints

- Commit messages: terse conventional style (`fix:`, `feat:`, `refactor:`, `test:`), **no Co-Authored-By trailer, no AI prose** (user preference).
- Run `gofmt -l .` before every commit; it must output nothing.
- Unit tests must not require Docker. Anything touching Neo4j goes in `//go:build integration` files and uses `internal/testutil.Neo4j`.
- Integration test command: `go test -tags=integration ./internal/graph/... ./internal/catalog/...` (needs Docker; on Podman prefix `TESTCONTAINERS_RYUK_DISABLED=true`).
- Secrets stay in env vars, never flags.
- Do not change exported API beyond what each task states.
- The Grafana plugin was already removed (staged in git before this plan); do not reference `plugin/` anywhere.

---

### Task 1: Clamp close-validity timestamps in the graph layer

The retraction path (`CloseAppValidity`, called by the loader's reconcile pass) sets `validTo=$at` unconditionally. If `$at` is earlier than an open edge's `validFrom` (non-monotonic git commit times, mixed wall-clock/commit-time runs), the edge gets `validTo < validFrom` — a window no point-in-time query can match, so the app silently vanishes from all history. Fix: clamp each close to `max($at, validFrom+1)` inside the existing Cypher.

**Files:**
- Modify: `internal/graph/repo.go` (function `closeAppValidityTx`, ~lines 305–338)
- Test: `internal/graph/close_clamp_test.go` (new)

**Interfaces:**
- Consumes: existing `Repo.ReloadDeclaredApp`, `Repo.CloseAppValidity`, `Repo.AppPath`, test helpers `seedIface`/`buildReloadApp` in `internal/graph/reload_test.go` (integration-tagged, same package — reusable directly).
- Produces: no signature changes. New invariant: after any close, every closed edge satisfies `validTo > validFrom`.

- [ ] **Step 1: Write the failing integration test**

Create `internal/graph/close_clamp_test.go`:

```go
//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// TestCloseValidityClampsToValidFrom verifies that closing an app's validity
// with a timestamp EARLIER than the open edges' validFrom does not produce an
// inverted window (validTo < validFrom). The close must clamp to validFrom+1
// so the historical window stays queryable.
func TestCloseValidityClampsToValidFrom(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:clamp-a", "rtr-clamp-a", "te0/0/1")

	t10 := time.Unix(1700001000, 0).UTC()
	t5 := time.Unix(1700000500, 0).UTC() // EARLIER than t10

	d := buildReloadApp(t10, "interface:clamp-a", "rtr-clamp-a")
	d.AppPHash, d.App = "application:clamp-test", "clamp-test"
	d.AppSvcPHash, d.AppSvc = "appservice:clamp-test", "clamp-test"
	if err := r.ReloadDeclaredApp(ctx, d, t10); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Retract with an out-of-order (earlier) timestamp.
	if err := r.CloseAppValidity(ctx, d.AppPHash, t5); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Every closed TAKES / DEPENDS_ON / Connection must satisfy validTo > validFrom.
	res, err := neo4j.ExecuteQuery(ctx, drv,
		`MATCH (:Application {phash:$p})-[:RUNS_AS]->(svc)
		 OPTIONAL MATCH (svc)-[do:DEPENDS_ON]->()
		 OPTIONAL MATCH (svc)-[:USES]->(conn:Connection)-[tk:TAKES]->()
		 RETURN do.validFrom AS dof, do.validTo AS dot,
		        conn.validFrom AS cf, conn.validTo AS ct,
		        tk.validFrom AS tf, tk.validTo AS tt`,
		map[string]any{"p": d.AppPHash},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase("neo4j"))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, rec := range res.Records {
		for _, pair := range [][2]string{{"dof", "dot"}, {"cf", "ct"}, {"tf", "tt"}} {
			fromV, _ := rec.Get(pair[0])
			toV, _ := rec.Get(pair[1])
			from, ok1 := fromV.(int64)
			to, ok2 := toV.(int64)
			if !ok1 || !ok2 {
				continue // absent optional edge
			}
			if to <= from {
				t.Errorf("%s/%s: inverted window validFrom=%d validTo=%d", pair[0], pair[1], from, to)
			}
		}
	}

	// The original window must still be queryable at its validFrom instant.
	hops, err := r.AppPath(ctx, d.AppPHash, t10)
	if err != nil {
		t.Fatalf("AppPath(t10): %v", err)
	}
	if len(hops) != 1 {
		t.Fatalf("AppPath(t10): want 1 hop (window [t10,t10+1) queryable), got %d", len(hops))
	}
	// And closed one second later.
	hops, err = r.AppPath(ctx, d.AppPHash, t10.Add(time.Second))
	if err != nil {
		t.Fatalf("AppPath(t10+1): %v", err)
	}
	if len(hops) != 0 {
		t.Fatalf("AppPath(t10+1): want 0 hops after close, got %d", len(hops))
	}
}
```

Note: `buildReloadApp` hardcodes app identity fields; the test overrides them after the call so it cannot collide with other tests sharing the container. Check `reload_test.go` — if `buildReloadApp`'s signature differs from what's shown here, adapt the call, not the helper.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestCloseValidityClampsToValidFrom -v`
Expected: FAIL with "inverted window" errors (current code writes `validTo=t5 < validFrom=t10`).

- [ ] **Step 3: Clamp the three close statements**

In `internal/graph/repo.go`, replace the three Cypher statements inside `closeAppValidityTx` (keep the surrounding Go error handling exactly as is; only the query strings change):

Statement 1 (TAKES + its Connection):

```cypher
MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
MATCH (svc)-[:USES]->(conn:Connection)-[t:TAKES]->(:Path)
WHERE t.validTo IS NULL
SET t.validTo = CASE WHEN $at > t.validFrom THEN $at ELSE t.validFrom + 1 END,
    conn.validTo = CASE WHEN $at > conn.validFrom THEN $at ELSE conn.validFrom + 1 END
```

Statement 2 (Connections with no open TAKES):

```cypher
MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
MATCH (svc)-[:USES]->(conn:Connection)
WHERE conn.validTo IS NULL
SET conn.validTo = CASE WHEN $at > conn.validFrom THEN $at ELSE conn.validFrom + 1 END
```

Statement 3 (DEPENDS_ON):

```cypher
MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
MATCH (svc)-[do:DEPENDS_ON]->()
WHERE do.validTo IS NULL
SET do.validTo = CASE WHEN $at > do.validFrom THEN $at ELSE do.validFrom + 1 END
```

Extend the function's doc comment with one sentence:

```
// The close clamps to validFrom+1 when at is not after validFrom, so an
// out-of-order timestamp (non-monotonic commit times, mixed manual/CI runs)
// can never produce an inverted [validFrom, validTo) window.
```

- [ ] **Step 4: Run the new test and the full graph integration suite**

Run: `go test -tags=integration ./internal/graph/ -v`
Expected: PASS, including all pre-existing reload/reconcile/idempotency tests (the clamp is a no-op for the normal `at > validFrom` case).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && git add internal/graph/repo.go internal/graph/close_clamp_test.go
git commit -m "fix(graph): clamp close-validity timestamps to prevent inverted windows"
```

---

### Task 2: Reload monotonicity guard must consider closed edges

`ReloadDeclaredApp` bumps its effective timestamp past the max `validFrom` of **open** DEPENDS_ON edges only. After a retraction (all edges closed), `prev` is null, so re-declaring with an earlier timestamp opens a window that overlaps closed history — a point-in-time query inside the overlap returns hops from two revisions.

**Files:**
- Modify: `internal/graph/repo.go` (function `ReloadDeclaredApp`, ~lines 363–396)
- Test: `internal/graph/close_clamp_test.go` (append)

**Interfaces:**
- Consumes: Task 1's clamped `closeAppValidityTx` (this task's test retracts first).
- Produces: no signature changes. New invariant: a reload's effective `validFrom` is strictly greater than every previously written `validFrom` and `validTo` of the app's declared edges.

- [ ] **Step 1: Write the failing integration test**

Append to `internal/graph/close_clamp_test.go`:

```go
// TestReloadAfterRetractionNoOverlap verifies that re-declaring an app with a
// timestamp EARLIER than its retraction does not open a window overlapping the
// closed history: no single instant may return hops from two revisions.
func TestReloadAfterRetractionNoOverlap(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	seedIface(t, ctx, r, "interface:ovl-a", "rtr-ovl-a", "te0/0/1")
	seedIface(t, ctx, r, "interface:ovl-b", "rtr-ovl-b", "te0/0/2")

	t0 := time.Unix(1700000000, 0).UTC()
	t1 := time.Unix(1700001000, 0).UTC()
	t2 := time.Unix(1700002000, 0).UTC()

	d1 := buildReloadApp(t1, "interface:ovl-a", "rtr-ovl-a")
	d1.AppPHash, d1.App = "application:ovl-test", "ovl-test"
	d1.AppSvcPHash, d1.AppSvc = "appservice:ovl-test", "ovl-test"
	if err := r.ReloadDeclaredApp(ctx, d1, t1); err != nil {
		t.Fatalf("load rev1: %v", err)
	}
	if err := r.CloseAppValidity(ctx, d1.AppPHash, t2); err != nil {
		t.Fatalf("retract: %v", err)
	}

	// Re-declare with a hop change and an OUT-OF-ORDER timestamp t0 < t1 < t2.
	d2 := buildReloadApp(t0, "interface:ovl-b", "rtr-ovl-b")
	d2.AppPHash, d2.App = "application:ovl-test", "ovl-test"
	d2.AppSvcPHash, d2.AppSvc = "appservice:ovl-test", "ovl-test"
	if err := r.ReloadDeclaredApp(ctx, d2, t0); err != nil {
		t.Fatalf("reload rev2: %v", err)
	}

	// Mid-first-window: only revision 1's hop.
	hops, err := r.AppPath(ctx, d1.AppPHash, t1.Add(time.Second))
	if err != nil {
		t.Fatalf("AppPath(t1+1): %v", err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-ovl-a" {
		t.Fatalf("AppPath(t1+1): want exactly rev1 hop (rtr-ovl-a), got %+v", hops)
	}

	// Well after the retraction: only revision 2's hop.
	hops, err = r.AppPath(ctx, d1.AppPHash, t2.Add(time.Hour))
	if err != nil {
		t.Fatalf("AppPath(t2+1h): %v", err)
	}
	if len(hops) != 1 || hops[0].Device != "rtr-ovl-b" {
		t.Fatalf("AppPath(t2+1h): want exactly rev2 hop (rtr-ovl-b), got %+v", hops)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestReloadAfterRetractionNoOverlap -v`
Expected: FAIL at the `AppPath(t1+1)` assertion — with no guard, revision 2's window starts at t0 and overlaps, so two hops come back.

- [ ] **Step 3: Widen the monotonicity read**

In `ReloadDeclaredApp`, replace the `prev` query:

```go
res, err := tx.Run(ctx,
	`MATCH (app:Application {phash:$p})-[:RUNS_AS]->(:ApplicationService)-[do:DEPENDS_ON]->()
	 RETURN max(coalesce(do.validTo, do.validFrom)) AS prev`,
	map[string]any{"p": d.AppPHash})
```

(The change: the `WHERE do.validTo IS NULL` filter is removed and the aggregate becomes `max(coalesce(do.validTo, do.validFrom))`, so closed edges push the floor via their `validTo`.)

Update the surrounding comment: the read now returns the latest instant any declared edge has touched — open edges via `validFrom`, closed edges via `validTo` — so a reload after retraction also lands strictly after all history. The existing bump logic (`if effUnix <= prevUnix { effUnix = prevUnix + 1 }`) is unchanged. Note in the comment that this leaves a deliberate 1-second retracted gap between a close at T and a re-declaration bumped to T+1.

- [ ] **Step 4: Run the new test and the full graph integration suite**

Run: `go test -tags=integration ./internal/graph/ -v`
Expected: PASS. Pay attention to `TestSameSecondReloadKeepsQueryableWindow` and `TestDependsOnAppendOnlyAcrossReloads` — they exercise the same bump logic and must still pass unchanged.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && git add internal/graph/repo.go internal/graph/close_clamp_test.go
git commit -m "fix(graph): reload monotonicity guard includes closed edges"
```

---

### Task 3: Batched interface upsert (`UpsertInterfaces`)

`catalog.Sync` currently issues one Neo4j write per interface row — thousands of sequential round trips per scheduled run. Add a batched UNWIND-based upsert; Task 4 wires it into Sync.

**Files:**
- Modify: `internal/graph/repo.go` (add below `UpsertInterface`)
- Test: `internal/graph/upsert_batch_test.go` (new)

**Interfaces:**
- Consumes: existing `Repo.write`, `Iface` struct.
- Produces: `func (r *Repo) UpsertInterfaces(ctx context.Context, ifaces []Iface) error` — semantics identical to calling `UpsertInterface` per element (MERGE on phash, overwrite properties, provenance "observed"). Task 4 calls this.

- [ ] **Step 1: Write the failing integration test**

Create `internal/graph/upsert_batch_test.go`:

```go
//go:build integration

package graph

import (
	"context"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/testutil"
)

func TestUpsertInterfacesBatch(t *testing.T) {
	ctx := context.Background()
	drv, cleanup := testutil.Neo4j(t, ctx)
	defer cleanup()
	r := New(drv, "neo4j")
	_ = r.EnsureConstraints(ctx)

	obs := time.Unix(1700000000, 0).UTC()
	batch := []Iface{
		{PHash: "interface:batch-1", Device: "rtr-b1", IfName: "te0/0/1", MetricIfName: "Te0/0/1",
			Instance: "10.1.0.1", IfIndex: 1, Vendor: "cisco", ObservedAt: obs},
		{PHash: "interface:batch-2", Device: "rtr-b1", IfName: "te0/0/2", MetricIfName: "Te0/0/2",
			Instance: "10.1.0.1", IfIndex: 2, Vendor: "cisco", ObservedAt: obs},
		{PHash: "interface:batch-3", Device: "rtr-b2", IfName: "ge-0/0/3", MetricIfName: "ge-0/0/3",
			Instance: "10.1.0.2", IfIndex: 3, Vendor: "juniper", ObservedAt: obs},
	}
	if err := r.UpsertInterfaces(ctx, batch); err != nil {
		t.Fatalf("UpsertInterfaces: %v", err)
	}

	// Idempotent re-run with an updated property must not duplicate nodes.
	batch[1].IfAlias = "uplink-renamed"
	if err := r.UpsertInterfaces(ctx, batch); err != nil {
		t.Fatalf("UpsertInterfaces (rerun): %v", err)
	}

	all, err := r.ListAllInterfaces(ctx)
	if err != nil {
		t.Fatalf("ListAllInterfaces: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 interfaces after idempotent rerun, got %d", len(all))
	}
	got, err := r.GetInterfaceByPHash(ctx, "interface:batch-2")
	if err != nil {
		t.Fatalf("GetInterfaceByPHash: %v", err)
	}
	if got.IfAlias != "uplink-renamed" {
		t.Fatalf("rerun did not overwrite properties: IfAlias=%q", got.IfAlias)
	}
	if !got.ObservedAt.Equal(obs) {
		t.Fatalf("ObservedAt round-trip: got %v want %v", got.ObservedAt, obs)
	}

	// Empty batch is a no-op, not an error.
	if err := r.UpsertInterfaces(ctx, nil); err != nil {
		t.Fatalf("UpsertInterfaces(nil): %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertInterfacesBatch -v`
Expected: FAIL to compile — `UpsertInterfaces` undefined.

- [ ] **Step 3: Implement the batched upsert**

Add to `internal/graph/repo.go`, directly below `UpsertInterface`:

```go
// upsertBatchSize bounds how many interface rows travel in one UNWIND write,
// keeping parameter payloads and transaction sizes moderate on large estates.
const upsertBatchSize = 500

// UpsertInterfaces performs the same per-row upsert as UpsertInterface for
// every element of ifaces, batched via UNWIND so a full catalog sync costs
// O(len/upsertBatchSize) round trips instead of one per interface.
func (r *Repo) UpsertInterfaces(ctx context.Context, ifaces []Iface) error {
	for start := 0; start < len(ifaces); start += upsertBatchSize {
		end := min(start+upsertBatchSize, len(ifaces))
		rows := make([]map[string]any, 0, end-start)
		for _, i := range ifaces[start:end] {
			rows = append(rows, map[string]any{
				"phash": i.PHash, "device": i.Device, "ifName": i.IfName,
				"metricIfName": i.MetricIfName, "ifDescr": i.IfDescr, "ifAlias": i.IfAlias,
				"instance": i.Instance, "vendor": i.Vendor, "ifIndex": i.IfIndex,
				"observedAt": i.ObservedAt.Unix()})
		}
		if err := r.write(ctx,
			`UNWIND $rows AS row
	         MERGE (n:Interface {phash:row.phash})
	         SET n.device=row.device, n.ifName=row.ifName, n.metricIfName=row.metricIfName,
	             n.ifDescr=row.ifDescr, n.ifAlias=row.ifAlias, n.instance=row.instance,
	             n.vendor=row.vendor, n.ifIndex=row.ifIndex, n.observedAt=row.observedAt,
	             n.provenance='observed'`,
			map[string]any{"rows": rows}); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertInterfacesBatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && git add internal/graph/repo.go internal/graph/upsert_batch_test.go
git commit -m "feat(graph): batched UpsertInterfaces via UNWIND"
```

---

### Task 4: Catalog sync — pure build step, port-stripping fallback, collision warning

Three findings in one refactor: (a) the last-resort device fallback uses the raw `instance` value, which is almost always `host:port` and therefore rejected by `SafeKey` — strip the port; (b) two rows collapsing to the same `(device, canonical ifName)` phash with different instances silently last-write-win — warn; (c) `Sync` should write via Task 3's batched upsert. Extract the row-conversion into a pure `buildIfaces` so all of it is unit-testable without Neo4j.

**Files:**
- Modify: `internal/catalog/sync.go` (full rewrite of `Sync`; add `buildIfaces`)
- Test: `internal/catalog/build_test.go` (new, unit — no build tag)

**Interfaces:**
- Consumes: `graph.Repo.UpsertInterfaces` (Task 3), existing `CanonicalIfName`, `phash.NormDevice`, `phash.SafeKey`, `ifacePHash`.
- Produces: `Sync` signature unchanged. New unexported `buildIfaces(rows []promclient.IfaceRow, devByInstance map[string]string, vendor string, now time.Time) builtCatalog` where `builtCatalog` has fields `ifaces []graph.Iface`, `skipped int`, `warnings []string`.

- [ ] **Step 1: Write the failing unit tests**

Create `internal/catalog/build_test.go`:

```go
package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

var buildNow = time.Unix(1700000000, 0).UTC()

func TestBuildIfacesDevicePrecedence(t *testing.T) {
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-labeled", IfName: "Te0/0/1", IfIndex: 1},
		{Instance: "10.0.0.2:9116", IfName: "Te0/0/2", IfIndex: 2}, // nautobot map
		{Instance: "10.0.0.3:9116", IfName: "Te0/0/3", IfIndex: 3}, // instance fallback
	}
	devMap := map[string]string{"10.0.0.2:9116": "rtr-nautobot"}
	b := buildIfaces(rows, devMap, "cisco", buildNow)
	if b.skipped != 0 {
		t.Fatalf("skipped=%d, warnings=%v; want 0 skips", b.skipped, b.warnings)
	}
	if len(b.ifaces) != 3 {
		t.Fatalf("want 3 ifaces, got %d", len(b.ifaces))
	}
	if b.ifaces[0].Device != "rtr-labeled" {
		t.Errorf("row 0: device label must win, got %q", b.ifaces[0].Device)
	}
	if b.ifaces[1].Device != "rtr-nautobot" {
		t.Errorf("row 1: nautobot map must win over instance, got %q", b.ifaces[1].Device)
	}
	// The critical fix: host:port instance fallback strips the port instead of
	// being rejected by SafeKey for containing ':'.
	if b.ifaces[2].Device != "10.0.0.3" {
		t.Errorf("row 2: instance fallback must strip the port, got %q", b.ifaces[2].Device)
	}
}

func TestBuildIfacesInstanceFallbackWithoutPort(t *testing.T) {
	rows := []promclient.IfaceRow{{Instance: "rtr-bare", IfName: "Te0/0/1", IfIndex: 1}}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if len(b.ifaces) != 1 || b.ifaces[0].Device != "rtr-bare" {
		t.Fatalf("portless instance must pass through unchanged, got %+v (skipped=%d)", b.ifaces, b.skipped)
	}
}

func TestBuildIfacesSkipsAndWarns(t *testing.T) {
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-1"}, // empty ifName AND ifDescr
	}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if b.skipped != 1 || len(b.ifaces) != 0 {
		t.Fatalf("want 1 skip 0 ifaces, got skipped=%d ifaces=%d", b.skipped, len(b.ifaces))
	}
	if len(b.warnings) != 1 || !strings.Contains(b.warnings[0], "empty canonical name") {
		t.Fatalf("want empty-canonical-name warning, got %v", b.warnings)
	}
}

func TestBuildIfacesCollisionWarning(t *testing.T) {
	// Two exporters scrape the same device name: identical (device, ifName)
	// phash, different instance. Last write wins, but it must warn.
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-dup", IfName: "Te0/0/1", IfIndex: 1},
		{Instance: "10.0.0.9:9116", Device: "rtr-dup", IfName: "Te0/0/1", IfIndex: 1},
	}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if b.skipped != 0 {
		t.Fatalf("collisions are not skips: skipped=%d", b.skipped)
	}
	found := false
	for _, w := range b.warnings {
		if strings.Contains(w, "identity collision") &&
			strings.Contains(w, "10.0.0.1:9116") && strings.Contains(w, "10.0.0.9:9116") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want identity-collision warning naming both instances, got %v", b.warnings)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/catalog/ -run TestBuildIfaces -v`
Expected: FAIL to compile — `buildIfaces` undefined.

- [ ] **Step 3: Implement `buildIfaces` and rewrite `Sync`**

Replace the body of `internal/catalog/sync.go` (keep the package comment location in `canon.go`; this file keeps its imports plus `"fmt"` and `"net"`):

```go
package catalog

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

// ifacePHash is the canonical interface identity (device + canonical ifName).
func ifacePHash(device, canonicalIfName string) string {
	return phash.Hash(phash.KindIface, device, canonicalIfName)
}

// builtCatalog is the pure outcome of converting harvested rows into Interface
// nodes: the rows to persist, how many were skipped, and human-readable
// warnings (skips and identity collisions) for the caller to log.
type builtCatalog struct {
	ifaces   []graph.Iface
	skipped  int
	warnings []string
}

// buildIfaces converts harvested Prometheus rows into Interface nodes without
// touching the graph, so the conversion rules are unit-testable.
//
// Device-name precedence per row: the harvested device label wins; then the
// optional Nautobot instance→device map; then the HOST PART of the raw
// instance as a last resort (a Prometheus instance is almost always
// "host:port", and the raw value would always be rejected by SafeKey for
// containing ':').
//
// Rows whose normalized device or canonical ifName still contain ':' or
// control characters, or that yield an empty canonical name, are skipped with
// a warning. Two rows collapsing to the same identity phash from different
// instances (e.g. two exporters scraping one device name) produce an
// identity-collision warning; the later row wins, matching MERGE semantics.
func buildIfaces(rows []promclient.IfaceRow, devByInstance map[string]string, vendor string, now time.Time) builtCatalog {
	var b builtCatalog
	firstInstance := map[string]string{} // phash -> first instance seen
	for _, row := range rows {
		raw := row.Device
		if raw == "" {
			raw = devByInstance[row.Instance]
		}
		if raw == "" {
			raw = row.Instance
			if host, _, err := net.SplitHostPort(raw); err == nil {
				raw = host
			}
		}
		dev := phash.NormDevice(raw)
		canon := CanonicalIfName(vendor, row.IfName)
		if canon == "" {
			canon = CanonicalIfName(vendor, row.IfDescr)
		}
		if canon == "" {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"skipping row with empty canonical name (device=%q ifName=%q ifDescr=%q)",
				dev, row.IfName, row.IfDescr))
			b.skipped++
			continue
		}
		if err := phash.SafeKey("device", dev); err != nil {
			b.warnings = append(b.warnings, fmt.Sprintf("skipping row (instance=%q): %v", row.Instance, err))
			b.skipped++
			continue
		}
		if err := phash.SafeKey("ifName", canon); err != nil {
			b.warnings = append(b.warnings, fmt.Sprintf("skipping row (instance=%q ifName=%q): %v", row.Instance, canon, err))
			b.skipped++
			continue
		}
		ph := ifacePHash(dev, canon)
		if prev, seen := firstInstance[ph]; seen && prev != row.Instance {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"identity collision: (device=%q ifName=%q) harvested from instances %q and %q; last write wins",
				dev, canon, prev, row.Instance))
		} else if !seen {
			firstInstance[ph] = row.Instance
		}
		b.ifaces = append(b.ifaces, graph.Iface{
			PHash: ph, Device: dev, IfName: canon,
			MetricIfName: row.IfName, IfDescr: row.IfDescr, IfAlias: row.IfAlias,
			Instance: row.Instance, Vendor: vendor, IfIndex: row.IfIndex, ObservedAt: now})
	}
	return b
}

// Sync turns harvested Prometheus rows into Interface nodes, keyed by canonical
// (device, ifName), binding the real metric labels and current ifIndex. Rows
// are converted by buildIfaces (see its doc for precedence and skip rules) and
// written in batches via UpsertInterfaces.
func Sync(ctx context.Context, r *graph.Repo, rows []promclient.IfaceRow,
	devByInstance map[string]string, vendor string) error {
	b := buildIfaces(rows, devByInstance, vendor, time.Now().UTC())
	for _, w := range b.warnings {
		log.Printf("catalog.Sync: %s", w)
	}
	if err := r.UpsertInterfaces(ctx, b.ifaces); err != nil {
		return err
	}
	if b.skipped > 0 {
		log.Printf("catalog.Sync: skipped %d row(s) with invalid device/ifName", b.skipped)
	}
	return nil
}
```

- [ ] **Step 4: Run unit tests, then the full suites**

Run: `go test ./internal/catalog/ -v`
Expected: PASS (new build tests plus all existing resolver/canon tests).

Run: `go test -tags=integration ./internal/catalog/... -v` (if the package has integration-tagged Sync tests — check with `grep -rl "go:build integration" internal/catalog/`)
Expected: PASS.

- [ ] **Step 5: Update the README fallback description**

In `README.md`, section "### `promhash-catalog`: build the interface catalog", the sentence "then the raw `instance` value as a last resort" becomes "then the host part of the raw `instance` value as a last resort".

- [ ] **Step 6: Commit**

```bash
gofmt -l . && git add internal/catalog/sync.go internal/catalog/build_test.go README.md
git commit -m "fix(catalog): strip port from instance device fallback, warn on identity collisions, batch sync writes"
```

---

### Task 5: Alert proxy request-body limit

`Proxy.ServeHTTP` does an unbounded `io.ReadAll(r.Body)`. Cap it with `http.MaxBytesReader` and answer 413 when exceeded.

**Files:**
- Modify: `internal/alertenrich/proxy.go` (`ServeHTTP`, ~lines 79–90)
- Test: `internal/alertenrich/proxy_test.go` (append; reuse the file's existing proxy/upstream test helpers — read the file first and follow its construction pattern)

**Interfaces:**
- Consumes: existing `NewProxy`, test fakes in `proxy_test.go`.
- Produces: no signature changes. New behavior: bodies larger than 8 MiB → HTTP 413, nothing forwarded.

- [ ] **Step 1: Write the failing test**

Append to `internal/alertenrich/proxy_test.go` (adapt the proxy construction to the file's existing helper — the essential shape is):

```go
func TestServeHTTPRejectsOversizedBody(t *testing.T) {
	forwarded := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
	}))
	defer up.Close()

	p := NewProxy(ProxyConfig{
		Client:     stubClient{}, // the file's existing no-op ImpactClient fake
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestServeHTTPRejectsOversizedBody -v`
Expected: FAIL to compile (`maxAlertBodyBytes` undefined) — then after a temporary stub const, FAIL with status 400 vs 413. (Compile failure alone is acceptable proof; proceed.)

- [ ] **Step 3: Implement the cap**

In `internal/alertenrich/proxy.go`, add near the top of the file:

```go
// maxAlertBodyBytes caps the accepted POST body. Real Alertmanager batches
// are far smaller; anything bigger is a bug or abuse, and reading it
// unbounded would let one client exhaust memory.
const maxAlertBodyBytes = 8 << 20
```

And change the start of `ServeHTTP`:

```go
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxAlertBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			http.Error(w, "alert payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	// ... rest unchanged
```

(`errors` is already imported in this file.)

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/alertenrich/ -v`
Expected: PASS, including all pre-existing proxy tests.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && git add internal/alertenrich/proxy.go internal/alertenrich/proxy_test.go
git commit -m "fix(alertenrich): cap alert batch body at 8MiB"
```

---

### Task 6: Bounded concurrent enrichment in the alert proxy

`ServeHTTP` enriches alerts serially; worst-case batch latency is `len(alerts) × Timeout` (100 alerts × 2s = 200s), far beyond the Prometheus notifier timeout — precisely during alert storms. Enrich with bounded parallelism. (An in-batch lookup memo was considered and rejected: lookups are keyed by each alert's `startsAt`, which differs across alert instances, so the hit rate would be negligible — YAGNI.)

**Files:**
- Modify: `internal/alertenrich/proxy.go` (`ServeHTTP`)
- Test: `internal/alertenrich/proxy_test.go` (append)

**Interfaces:**
- Consumes: `enrichOne` (unchanged — each call touches only its own alert; the metrics and `lookupCache` are already concurrency-safe).
- Produces: no signature changes. New behavior: up to 16 lookups in flight per batch; batch order of alerts in the forwarded payload is unchanged (goroutines mutate alerts in place, the slice is marshaled after `wg.Wait()`).

- [ ] **Step 1: Write the failing test**

Append to `internal/alertenrich/proxy_test.go`:

```go
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
```

(Adjust imports of the test file: `sync/atomic`, `fmt`, `encoding/json` as needed.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/alertenrich/ -run TestServeHTTPEnrichesConcurrently -v`
Expected: FAIL — `max concurrent lookups = 1` with the current serial loop.

- [ ] **Step 3: Implement bounded parallelism**

In `internal/alertenrich/proxy.go`, add the constant and replace the enrichment loop in `ServeHTTP`:

```go
// maxConcurrentEnrich bounds in-flight impact lookups per batch. Serial
// enrichment makes worst-case batch latency len(alerts)×Timeout — minutes
// during exactly the alert storms the proxy exists for; the sender's
// notification deadline is single-digit seconds.
const maxConcurrentEnrich = 16
```

```go
	sem := make(chan struct{}, maxConcurrentEnrich)
	var wg sync.WaitGroup
	for _, a := range alerts {
		wg.Add(1)
		sem <- struct{}{}
		go func(a rawAlert) {
			defer wg.Done()
			defer func() { <-sem }()
			p.enrichOne(r.Context(), a)
		}(a)
	}
	wg.Wait()
```

Add `"sync"` to the file's imports. `enrichOne` itself is untouched: each goroutine mutates only its own `rawAlert` map, the Prometheus metrics are atomic, and `lookupCache` is mutex-guarded.

- [ ] **Step 4: Run the package tests, including the race detector**

Run: `go test -race ./internal/alertenrich/ -v`
Expected: PASS with no race reports. The `-race` run is mandatory for this task.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && git add internal/alertenrich/proxy.go internal/alertenrich/proxy_test.go
git commit -m "perf(alertenrich): enrich alert batches with bounded concurrency"
```

---

### Task 7: Batch service-name lookup for `/mapping.prom`

Every evaluator scrape currently runs 2 Neo4j queries per curated app (`AppPath` + `AppServiceName`). Replace the per-app `AppServiceName` with one batched `AppServiceNames` query (N+1 → N+1 becomes N+1 with N AppPath calls and 1 name query). The all-or-nothing 500 on any app error is retained deliberately — the generated `PromhashMappingAbsent` alert covers a broken exposition, and partial output would silently mis-attribute; add a comment saying so.

**Files:**
- Modify: `internal/graph/repo.go` (replace `AppServiceName` with `AppServiceNames`)
- Modify: `internal/api/server.go` (Repo interface + `mappingProm`)
- Test: `internal/api/server_test.go` (update `fakeRepo`, existing mapping tests)

**Interfaces:**
- Consumes: nothing new.
- Produces: `func (r *Repo) AppServiceNames(ctx context.Context, apps []string) (map[string]string, error)` — one entry per app that has a `RUNS_AS` service; apps without one are absent (callers fall back to the app name). The `api.Repo` interface swaps `AppServiceName(ctx, app) (string, error)` for `AppServiceNames(ctx, apps []string) (map[string]string, error)`. The old per-app `AppServiceName` method is deleted (its only caller was `mappingProm`).

- [ ] **Step 1: Update the fake and write/adjust the tests**

In `internal/api/server_test.go`: replace the `fakeRepo` method

```go
func (fakeRepo) AppServiceNames(_ context.Context, apps []string) (map[string]string, error) {
	out := map[string]string{}
	for _, app := range apps {
		out[app] = app + "-svc"
	}
	return out, nil
}
```

(Match the existing fake's naming convention — read the current `AppServiceName` fake first and keep its service-name derivation so existing mapping-endpoint assertions still hold; if the current fake returns something other than `app+"-svc"`, keep that form.)

Add one test asserting the batch call happens once per request (extend the fake with a counter):

```go
func TestMappingPromBatchesServiceNames(t *testing.T) {
	repo := &countingRepo{} // embeds the fake, increments serviceNameCalls
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
```

(Concrete `countingRepo`: embed the existing fake struct, override `AppServiceNames` to increment the counter then delegate.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/api/ -v`
Expected: FAIL to compile — interface/fake mismatch drives the change.

- [ ] **Step 3: Implement `AppServiceNames` in the graph layer**

In `internal/graph/repo.go`, delete `AppServiceName` and add:

```go
// AppServiceNames returns app-name → service-name for every app in apps that
// has a RUNS_AS service. Apps without one are absent from the map; callers
// fall back to the app name. When an app has multiple RUNS_AS services the
// last row wins (mirrors the LIMIT 1 of the former per-app lookup).
func (r *Repo) AppServiceNames(ctx context.Context, apps []string) (map[string]string, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`UNWIND $apps AS app
	     MATCH (:Application {name:app})-[:RUNS_AS]->(s:ApplicationService)
	     RETURN app, s.name AS svc`,
		map[string]any{"apps": apps},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(res.Records))
	for _, rec := range res.Records {
		a, _ := rec.Get("app")
		s, _ := rec.Get("svc")
		app, _ := a.(string)
		svc, _ := s.(string)
		out[app] = svc
	}
	return out, nil
}
```

- [ ] **Step 4: Rewire the API server**

In `internal/api/server.go`: in the `Repo` interface, replace the `AppServiceName` method declaration with:

```go
	// AppServiceNames returns app -> service name for the given apps; apps
	// with no recorded service are absent (callers fall back to the app name).
	AppServiceNames(ctx context.Context, apps []string) (map[string]string, error)
```

Rewrite `mappingProm`:

```go
func (s *Server) mappingProm(w http.ResponseWriter, r *http.Request) {
	appsParam := strings.TrimSpace(r.URL.Query().Get("apps"))
	if appsParam == "" {
		http.Error(w, "apps query parameter is required (comma-separated curated app names)", http.StatusBadRequest)
		return
	}
	var apps []string
	for _, app := range strings.Split(appsParam, ",") {
		if app = strings.TrimSpace(app); app != "" {
			apps = append(apps, app)
		}
	}
	// One batched name lookup per scrape instead of one query per app.
	svcNames, err := s.repo.AppServiceNames(r.Context(), apps)
	if err != nil {
		log.Printf("api: mapping AppServiceNames: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	t := time.Now()
	var points []enrich.MappingPoint
	for _, app := range apps {
		hops, err := s.repo.AppPath(r.Context(), phash.Hash(phash.KindApp, app), t)
		if err != nil {
			// Deliberately fail-closed for the WHOLE exposition: partial output
			// would silently drop apps (mis-attribution); an error surfaces as a
			// failed scrape, which PromhashMappingAbsent/ScrapeDown page on.
			log.Printf("api: mapping AppPath(%s): %v", app, err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(hops) == 0 {
			continue
		}
		svc, ok := svcNames[app]
		if !ok {
			svc = app
		}
		points = append(points, enrich.MappingSeries(app, svc, hops, enrich.JoinByIfName)...)
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(enrich.RenderMappingSeries(points)))
}
```

- [ ] **Step 5: Run the affected suites**

Run: `go build ./... && go test ./internal/api/ ./internal/graph/ -v` and `go test -tags=integration ./internal/graph/ -run TestAppService -v` (adjust `-run` to whatever integration tests covered `AppServiceName`; update them to the batch method — same assertions through the map).
Expected: PASS everywhere; no remaining references to `AppServiceName` (`grep -rn "AppServiceName\b" --include='*.go' .` returns only `AppServiceNames`).

- [ ] **Step 6: Commit**

```bash
gofmt -l . && git add internal/graph/repo.go internal/api/server.go internal/api/server_test.go internal/graph/
git commit -m "perf(api): batch service-name lookup for /mapping.prom"
```

---

### Task 8: Remove dead `CapacityStatus` code

`promclient.CapacityStatus`, `CapRow`, `capKey`, and the two query constants have no callers outside their own tests (~75 lines of maintained-but-unused merge logic). Capacity data already reaches users via the `app:if_capacity_bps` recording rule; if harvest-side capacity is ever needed, git history has it.

**Files:**
- Modify: `internal/promclient/prom.go` (delete `CapRow`, `capSpeedQuery`, `capStatusQuery`, `capKey`, `CapacityStatus`; drop the `sort` import if now unused)
- Modify: `internal/promclient/prom_test.go` (delete the `CapacityStatus`/`CapRow` tests — find them with `grep -n "CapacityStatus\|CapRow" internal/promclient/prom_test.go`)

**Interfaces:**
- Consumes: nothing.
- Produces: `HarvestInterfaces` and the retry plumbing remain untouched.

- [ ] **Step 1: Verify dead-ness, delete, verify build**

Run first: `grep -rn "CapacityStatus\|CapRow" --include='*.go' . | grep -v _test` — must show only `internal/promclient/prom.go` definitions. Then delete the code and tests.

- [ ] **Step 2: Run the package tests**

Run: `go build ./... && go vet ./... && go test ./internal/promclient/ -v`
Expected: PASS; remaining tests (harvest, retry) untouched.

- [ ] **Step 3: Commit**

```bash
gofmt -l . && git add internal/promclient/
git commit -m "refactor(promclient): remove unused CapacityStatus"
```

---

### Task 9: `-neo4j-db` flag on all Neo4j-connected binaries

Every binary hardcodes `graph.New(drv, "neo4j")`. Add a `-neo4j-db` flag (default `"neo4j"`, so behavior is unchanged) to the five binaries that open the graph.

**Files:**
- Modify: `cmd/promhash-api/main.go`, `cmd/promhash-catalog/main.go`, `cmd/promhash-loader/main.go`, `cmd/promhash-seed/main.go`, `cmd/promhash-enrich/main.go`
- Modify: `README.md` (Command-line tools section, shared-flags sentence)

**Interfaces:**
- Consumes: existing `graph.New(drv neo4j.DriverWithContext, db string)`.
- Produces: flag `-neo4j-db` on the five binaries.

- [ ] **Step 1: Add the flag to each binary**

In each `run()`, alongside the existing Neo4j flags:

```go
	var neoDB string
	flag.StringVar(&neoDB, "neo4j-db", "neo4j", "Neo4j database name")
```

and change the corresponding `graph.New(drv, "neo4j")` call to `graph.New(drv, neoDB)`. (`cmd/promhash-alert-proxy` and `cmd/promhash-demo-exporter` do not touch Neo4j — leave them alone.)

- [ ] **Step 2: Update README**

In `README.md`, section "## Command-line tools", change:

"All tools share the Neo4j connection flags `-neo4j` and `-neo4j-user`." →
"All tools share the Neo4j connection flags `-neo4j`, `-neo4j-user`, and `-neo4j-db` (default `neo4j`)."

- [ ] **Step 3: Build, vet, run the cmd tests**

Run: `go build ./... && go vet ./... && go test ./cmd/...`
Expected: PASS (the loader/enrich/api main tests don't exercise flag parsing but must keep compiling).

- [ ] **Step 4: Commit**

```bash
gofmt -l . && git add cmd/ README.md
git commit -m "feat(cmd): -neo4j-db flag for all graph-connected binaries"
```

---

### Task 10: Full verification sweep

**Files:** none new.

- [ ] **Step 1: Unit sweep with race detector**

Run: `gofmt -l .` (expect empty) then `make ci` (build + vet + test) then `go test -race ./internal/alertenrich/ ./internal/api/`
Expected: all PASS.

- [ ] **Step 2: Integration sweep**

Run: `go test -tags=integration ./...` (Podman: prefix `TESTCONTAINERS_RYUK_DISABLED=true`)
Expected: all PASS.

- [ ] **Step 3: Demo smoke (optional but recommended)**

Run: `cd demo && docker compose up -d --build`, wait ~2 minutes, then verify `curl -s -H 'Authorization: Bearer demo-token' localhost:8080/apps` returns apps and `curl -s 'localhost:9091/api/v1/query?query=promhash_interface_app'` returns series. Tear down with `docker compose down -v`.
Expected: mapping series present — proves the `mappingProm`/`AppServiceNames` rewrite end-to-end.

- [ ] **Step 4: Final commit if any stragglers**

Only if steps 1–3 required fixes; otherwise nothing to commit.

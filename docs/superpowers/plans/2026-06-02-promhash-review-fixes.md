# promhash Review-Fixes Implementation Plan

> **⚠️ SUPERSEDED IN PART (2026-06-02):** The maintainer chose **full incorporation** of `review-observability.md` + **commit to the data-plane reshape**. The authoritative plan is now [2026-06-02-promhash-merged-plan.md](2026-06-02-promhash-merged-plan.md). This file remains the **full bite-sized TDD detail** for the unchanged bug-fix workstreams (C, D, E[except E2], F, G, H, I) which the merged plan references. **Do not execute** Workstream A, Workstream B, Task E2, OPT-3/BL-1, OPT-4's "drop ifOperStatus", or the P2-staleness backlog item from this file — they are superseded/reshaped by the merged plan (RA, RB, RC, RE).

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 49 verified findings in `review.md` — first make the two headline outputs (curated per-app projection; zero-cardinality dashboard pattern) actually work end-to-end, then close the correctness/data-integrity/robustness gaps.

**Architecture:** Go monorepo (`github.com/AlectoTheFirst/promhash`) + a separate Grafana datasource plugin module (`github.com/AlectoTheFirst/promhash-datasource` under `plugin/`). Two modules, coupled only over HTTP. Neo4j is the graph store; Prometheus federation + recording rules are the generated artifacts. Tests use `//go:build integration` + testcontainers Neo4j 5.23 for graph paths, plain `go test` elsewhere.

**Tech Stack:** Go 1.26, neo4j-go-driver/v5, prometheus/client_golang, gopkg.in/yaml.v3, TypeScript + `@grafana/data`/`@grafana/runtime` 10.4.19, React.

**Verification basis:** Every finding below was re-read against current code at commit `ef77e44`. All 49 confirmed. Two factual corrections to `review.md` are folded in (flagged ⚠️ where they appear).

---

## How this plan is organised

The 49 findings cross 8 subsystems with heavy cross-cutting dependencies, so this is decomposed into **9 PR-sized workstreams (A–I)** plus a **decisions-required backlog**. Per the writing-plans guidance for multi-subsystem specs, each workstream is independently shippable and testable.

- **P0 workstreams (A, B, C)** are fully detailed (TDD bite-sized steps + code) — they sit on the critical path of the project's stated value.
- **P1/P2 workstreams (D–I)** are task-level: each task gives exact files, the concrete change, and a behavioral test assertion (the fix designs are already concrete; the engineer should still write the failing test first).
- **Backlog** items are large/architectural or speculative; each carries a design and a **DECISION REQUIRED** flag. Do not start them until the owning decision is made.

**A universal rule for this plan (the review's core lesson):** the existing tests assert *generated-string equality against golden files* and therefore pass despite the P0 bugs. **Every new/changed test in this plan must assert behavior** — selector/label-set intersection, dedup, order-independence, resolved identity, YAML round-trip, HTTP status, ctx cancellation — never just "the output string equals X".

---

## Decisions required before / during execution

These are genuine forks the maintainer owns. Recommendations are given; the plan assumes the recommendation unless overridden.

1. **Enrich: small fixes now vs. mapping-metric rewrite (OPT-3).** The mapping-metric + `group_left` model (`promhash_interface_app{...} 1` joined on `instance,ifIndex`) would *supersede the internals* of P0-1/P0-2/P1-enrich-dedup. **Recommendation: ship Workstream A (small fixes) now** — they make the shipped feature actually emit data and are cheap; treat OPT-3 as a separate v2 design. The design spec's "no `app` label on the infra firehose" law does **not** forbid OPT-3 (it's a bounded separate metric family). → Backlog item BL-1.

2. **Interface identity model.** Identity is currently `(device, canonicalIfName)`. Findings P1-rename / LOW-validity / LOW-dedup patch the rename/decommission gaps incrementally (large, needs node-level validity). The architectural alternative is to make `(device, ifIndex)` the **primary** interface key, which would dissolve P1-rename entirely and neutralize P1-emptyref for identity. **Recommendation: ship the cheap identity-consistency fixes (Workstream F) now; defer rename/validity to a decision.** → Backlog item BL-2.

3. **`/interface-apps`: remove vs. alias.** It runs the identical query to `/impact`. ⚠️ The Grafana plugin's impact query calls `/interface-apps` (`plugin/.../pkg/plugin/query.go`), so **deleting the route breaks the plugin**. **Recommendation: collapse onto one shared handler but keep `/interface-apps` as an alias route** (one function, two routes). Baked into Workstream C.

4. **`criticality` field: remove vs. populate.** Nothing writes `Application.criticality`; `/impact` always returns `""` while the README advertises `"tier-1"`. **Recommendation: remove from model + docs for v1** (no upstream source of truth). Baked into Workstream E (P1-criticality). Re-add coherently if/when OPT-12 lands.

5. **History/index strategy.** OPT-9 (archive closed subgraphs off the live `:USES`/`:TAKES` traversal) **supersedes** OPT-8 (relationship range indexes). Pick one, not both. Both are forward-looking; current per-app fan-out is tiny. → Backlog (BL-3). Only OPT-6 (`app_name_unique`, backs a live query) ships now, in Workstream E.

---

## Dependency / sequencing overview

```
P0 (do first — unblocks the product):
  A  enrich data-correctness ............ self-contained; FederationMatch return type is the hub
  B  zero-cardinality variables E2E ..... B needs enrich.Selectors (factored in A/C) + api at()-bounds
  C  api canonicalization + handler ..... OPT-14 is keystone; P0-4/empty-params/iface-null fold into it

P1 (correctness / data integrity):
  D  declare/loader temporal integrity .. P1-atomic is keystone → P1-now → P1-retract(+LOW-glob); P1-strict, LOW-dep-nopath
  E  graph temporal invariant ........... P1-dependson (ship standalone first); P1-criticality; OPT-6
  F  catalog/phash identity ............. LOW-keysafe keystone → P1-device-identity → P1-emptyref, LOW-abbrev, LOW-nomatch

P2 (robustness / ops):
  G  http client robustness ............. shared GET helper → P2-timeout, P2-pagination, LOW-retry, LOW-errbody, LOW-harvest-swallow, observedAt read-back
  H  api observability .................. P2-obs (depends on C)
  I  ci/build/docs hygiene .............. P2-secrets, LOW-fatal (run() refactor), LOW-seed-constraints, LOW-ci-pins, LOW-makefile

Backlog (decision-gated): BL-1 OPT-3 | BL-2 interface-identity model (P1-rename/LOW-validity/LOW-dedup) |
  BL-3 OPT-9 vs OPT-8 | OPT-10 ListApps cache | OPT-12 potential-vs-active impact | OPT-13 candidate identity |
  OPT-7 composite index | OPT-15 /apps pagination | P2-validate-only catalog snapshot | P2-staleness metric+retire
```

Recommended merge order: **P1-dependson (E, tiny, standalone) → A → C → B → D → rest of E → F → G → H → I**. A, C, B together restore the product; D/E/F harden data integrity; G/H/I are ops polish.

---

# Workstream A — enrich: make the curated path emit data (P0)

**Single coordinated PR.** P0-2 changes `FederationMatch`'s return type and `TenantScrapeConfig`'s signature (the hub of a signature ripple), and P0-1 + P1-enrich-dedup + LOW-enrich-order + LOW-enrich-escape all rewrite the same `rules.go` emission loop. Doing them piecemeal re-touches the same functions and golden files repeatedly. Regenerate `payments.golden.yaml`/`tenant.golden.yaml` **once** at the end.

**Files (whole workstream):**
- Modify: `internal/enrich/rules.go`, `internal/enrich/federation.go`, `internal/enrich/tenant.go`, `cmd/promhash-enrich/main.go`
- Test: `internal/enrich/rules_test.go`, `internal/enrich/federation_test.go`, `internal/enrich/tenant_test.go`
- Regenerate: `internal/enrich/testdata/payments.golden.yaml`, `internal/enrich/testdata/tenant.golden.yaml`
- Docs: `README.md` (§356–360, 396), `docs/deploy/federation-tenant.md`

### Task A1: Refactor `RuleGroup` into collect → dedup → sort → write, dropping the `job=` matcher

Combines **P0-1** (drop `job=`), **P1-enrich-dedup** (dedup by `(dir,instance,ifIndex)`), **LOW-enrich-order** (deterministic sort), **LOW-enrich-escape** (YAML-safe values).

**Why P0-1 matters:** `rules.go:20` stamps `job="promhash-fed-<app>"` on every rule expr, but `tenant.go:14` sets `honor_labels: true`, so federated series keep their *original* `job` (`job="snmp"`) — the matcher selects zero series, the entire curated path produces no data. Masked because `rules_test.go` only string-compares the golden YAML.

- [ ] **Step 1: Write the failing behavioral test (intersection).** In `rules_test.go`, add `TestRuleGroupSelectorMatchesFederatedSeries`: build the federated label set a hop would carry post-federation — `{"__name__":"ifHCOutOctets","job":"snmp","instance":h.Instance,"ifIndex":strconv.Itoa(h.IfIndex)}`. Parse the matchers out of each generated expr (extract the `{...}` between `rate(METRIC` and `[5m]`). Assert every matcher key/value is satisfied by that label set (selector ⊆ federated labels) **and** that the selector contains no `job=` matcher.

```go
func TestRuleGroupSelectorMatchesFederatedSeries(t *testing.T) {
    hops := []graph.Hop{{Device: "rtr1", MetricIfName: "Te0/1", Instance: "10.0.0.5", IfIndex: 7, Direction: "egress"}}
    out := enrich.RuleGroup("payments", "svc", hops)
    federated := map[string]string{"__name__": "ifHCOutOctets", "job": "snmp", "instance": "10.0.0.5", "ifIndex": "7"}
    for _, m := range parseSelectorMatchers(t, out) { // helper you add: returns []struct{Key,Val string}
        if m.Key == "job" {
            t.Fatalf("rule selector must not match on job (honor_labels keeps original job); got job=%q", m.Val)
        }
        if federated[m.Key] != m.Val {
            t.Fatalf("selector %s=%q does not intersect federated labels %v", m.Key, m.Val, federated)
        }
    }
}
```

- [ ] **Step 2: Run it; confirm it fails.** `go test ./internal/enrich/ -run TestRuleGroupSelectorMatchesFederatedSeries -v` → FAIL (current expr contains `job="promhash-fed-payments"`).
- [ ] **Step 3: Add the dedup + order-independence + escape tests** (so the whole loop refactor is covered before you touch code):
  - `TestRuleGroupDedupsIdenticalHops`: pass the same `(instance,ifIndex,transit)` hop twice at different `Seq`; parse `(record,expr)` pairs from the YAML; assert no two are identical (exactly one ingress + one egress for that interface). Positive control: two different instances sharing an ifIndex both survive.
  - `TestRuleGroupOrderIndependent`: call `RuleGroup` with a hop slice and with the same slice reversed; assert byte-identical output.
  - `TestRuleGroupYAMLValid`: device `rtr,core`, ifName `Gi0/1 {x}`; `yaml.Unmarshal` the full output and assert it parses and `labels.device`/`labels.ifName` round-trip exactly.
- [ ] **Step 4: Run all four; confirm they fail** for the expected reasons.
- [ ] **Step 5: Implement the refactor.** Restructure `RuleGroup` so it: (a) expands hops into a slice of rule structs `{dir, metric, instance, ifIndex, device, ifName}` (transit → one ingress + one egress entry); (b) dedups by composite key `fmt.Sprintf("%s|%s|%d", dir, instance, ifIndex)`; (c) `sort.Slice` by `(dir, instance, ifIndex)` for full order-independence; (d) emits `expr: rate(%s{instance="%s", ifIndex="%d"}[5m])` — **no `job=`** — taking `(metric, instance, ifIndex)`; (e) routes every interpolated label value (`app/service/device/ifName`) and `instance` through a new `yamlFlowScalar(s string) string` helper that double-quotes + escapes when `s` contains any of `,{}[]:#&*!|>'"%@`` `, leading/trailing space, or is empty. Delete `job := "promhash-fed-"+app` and the `job` arg from `writeRule` (new signature `writeRule(b *strings.Builder, dir, metric, app, service string, h graph.Hop)`).
- [ ] **Step 6: Run the new tests; confirm PASS.**
- [ ] **Step 7: Regenerate `testdata/payments.golden.yaml`** (remove `job="promhash-fed-payments", ` from all 4 expr lines; apply new sort order) and update the `wantIn`/`wantOut` substrings in `TestRuleGroupTransitEmitsBothDirections`. Run `go test ./internal/enrich/` → all PASS.
- [ ] **Step 8: Commit.** `git commit -m "fix(enrich): drop job= matcher, dedup+sort+escape recording rules"`

> Optional durable alternative for LOW-enrich-escape: replace hand-built YAML with `yaml.Marshal` of a typed rule-group struct. Larger (invalidates golden formatting); the escaping helper is the proportionate v1 fix.

### Task A2: Per-hop exact `match[]` selectors + scrape-config emission

Combines **P0-2** (cross-product → per-hop exact selectors, `match[]` as a list), **LOW-enrich-quote** (single-quote escaping in the YAML list), **OPT-4** (emit the tenant scrape-config; stop federating `ifOperStatus`).

**Why P0-2 matters:** `federation.go:32-33` ANDs an instance-regex with an ifIndex-regex, so the selector matches the **cross product** `{instances}×{ifIndexes}`. ifIndex is not globally unique, so on a real backbone it over-pulls `I×X` instead of `N` real hops.

- [ ] **Step 1: Write the failing behavioral test.** In `federation_test.go`, `TestFederationMatchNoCrossProduct`: feed hops `{(10.0.0.1,42),(10.0.0.2,43),(10.0.0.1,42)}`; assert `FederationMatch` returns exactly **2** selectors (dedup); build the set of `(instance,ifIndex)` pairs the selectors would match and assert it equals exactly `{(10.0.0.1,42),(10.0.0.2,43)}` — i.e. `(10.0.0.1,43)` and `(10.0.0.2,42)` are NOT matchable. Also assert no selector contains `ifOperStatus`.
- [ ] **Step 2: Run; confirm fail** (current code returns one cross-product selector).
- [ ] **Step 3: Add `TestTenantScrapeConfigValidYAML`**: render `TenantScrapeConfig(app, mainProm, matches)` with `matches` containing an instance with a `'` (e.g. `10.0.0.1'odd`); `yaml.Unmarshal` and assert it parses and the recovered `match[]` list has `len(matches)` entries equal to the inputs. And `TestWriteArtifactsEmitsScrapeConfig` (see Step 6).
- [ ] **Step 4: Run; confirm fail.**
- [ ] **Step 5: Change `FederationMatch` to `func FederationMatch(hops []graph.Hop) []string`** returning one selector per unique `(instance,ifIndex)`: `{__name__=~"ifHC(In|Out)Octets", instance="<inst>", ifIndex="<idx>"}` (exact match, `ifOperStatus` dropped), deduped into a set and **sorted** deterministically. Instance values go through `yamlSingleQuote(s) = "'" + strings.ReplaceAll(s,"'","''") + "'"` where wrapped in the YAML list.
- [ ] **Step 6: Change `TenantScrapeConfig(app, mainProm string, matches []string) string`** to render `params:` → `'match[]':` as a YAML sequence (one `- '<selector>'` per match). Then in `cmd/promhash-enrich/main.go`: thread the `[]string` through (line ~57 writes one selector per line to `federate.match`); add a `-main-prom` flag (`flag.StringVar(&mainProm, "main-prom", "http://main-prometheus:9090", ...)`); after writing `rules.yaml`, call `TenantScrapeConfig` and `os.WriteFile` to `scrape.yaml`. Refactor the artifact loop into a testable `writeArtifacts(dir, app, svc, mainProm string, hops []graph.Hop) error` so `TestWriteArtifactsEmitsScrapeConfig` can assert all three files exist and `scrape.yaml` unmarshals with `job_name: promhash-fed-<app>` and a non-empty `match[]`.
- [ ] **Step 7: Run; PASS.** Regenerate `testdata/tenant.golden.yaml`; update `federation_test.go`/`tenant_test.go` goldens.
- [ ] **Step 8: Update docs.** `README.md` §356–360/396 (FederationMatch now returns a list; scrape-config is emitted; `ifOperStatus` removed) and `docs/deploy/federation-tenant.md`.
- [ ] **Step 9: Commit.** `git commit -m "fix(enrich): per-hop exact match[] selectors, emit scrape-config, drop ifOperStatus"`

> Note: A2 also factors the dedup logic that Workstream C (P0-3-backend) reuses as `enrich.Selectors` returning **raw** (un-escaped) values. If C lands first, do the `Selectors` extraction there and have `FederationMatch` call it; otherwise extract it here. Keep `FederationMatch`'s output byte-identical to its tests.

---

# Workstream B — zero-cardinality dashboard variables, end-to-end (P0-3)

**The README's marquee "free" pattern is unreachable.** Two breaks, both confirmed; one review claim corrected:

⚠️ **Correction:** review.md says the backend `/apps/{app}/selectors` endpoint "is implemented in the api subsystem." **It is not** — `internal/api/server.go` registers only `/apps`, `/apps/{app}/path`, `/interface-apps`, `/impact`. The endpoint must be **created** (it needs no new Neo4j query — `Repo.AppPath` already returns `Hop.Instance`/`Hop.IfIndex`). This is **Task B1** and is also listed in Workstream C's scope; do it once.

**Sequence:** B1 (backend selectors) → B2 (plugin resource routes) → B3 (plugin TS variable support) → B4 (docs). B1 depends on `LOW-at-bounds` (Workstream C, Task C5) for `at()` bounds; if C hasn't landed, B1's handler still works with the current `at()`.

### Task B1: `GET /apps/{app}/selectors` returning deduped+sorted `{instances,ifIndexes}`

**Files:** Modify `internal/api/server.go`, `internal/enrich/federation.go`; Test `internal/api/server_test.go`, `internal/enrich/federation_test.go`.

- [ ] **Step 1: Write failing test `TestSelectors` (enrich).** Hops with dup instances/ifIndexes and out-of-order values → assert `enrich.Selectors(hops)` returns deduped, **numerically-sorted** (`42` before `100`, not lexical), **raw** (un-escaped) slices.
- [ ] **Step 2: Write failing test `TestAppSelectors` (api).** `fakeRepo.AppPath` returns `[{Instance:"10.0.0.2",IfIndex:43},{Instance:"10.0.0.1",IfIndex:42},{Instance:"10.0.0.1",IfIndex:42}]`; `GET /apps/payments/selectors` → 200 with JSON `{"instances":["10.0.0.1","10.0.0.2"],"ifIndexes":["42","43"]}` (dedup → len 2 not 3). Empty path → `{"instances":[],"ifIndexes":[]}` (never `null`).
- [ ] **Step 3: Run; confirm both fail.**
- [ ] **Step 4: Factor `enrich.Selectors`.** `func Selectors(hops []graph.Hop) (instances []string, ifIndexes []string)`: dedup `h.Instance` (raw) and `strconv.Itoa(h.IfIndex)`; sort ifIndexes numerically (collect `[]int`, `sort.Ints`, format); `make([]string,0)` so JSON is `[]`. Rewrite `FederationMatch` to call `Selectors` then re-apply `regexp.QuoteMeta` per instance — **output byte-identical** to its existing test.
- [ ] **Step 5: Add the handler + route.** In `server.go`:

```go
func (s *Server) appSelectors(w http.ResponseWriter, r *http.Request) {
    t, ok := at(r)
    if !ok { http.Error(w, "invalid at parameter", http.StatusBadRequest); return }
    hops, err := s.repo.AppPath(r.Context(), phash.Hash(phash.KindApp, r.PathValue("app")), t)
    if err != nil { log.Printf("api: AppPath(selectors): %v", err); http.Error(w, "internal error", http.StatusInternalServerError); return }
    inst, idx := enrich.Selectors(hops)
    writeJSON(w, map[string][]string{"instances": inst, "ifIndexes": idx})
}
```
Register `s.mux.HandleFunc("GET /apps/{app}/selectors", s.appSelectors)`. Confirm no import cycle (`enrich` imports only `graph`, so `api → enrich` is clean).
- [ ] **Step 6: Run; PASS.** **Commit.** `git commit -m "feat(api): add /apps/{app}/selectors (deduped instance/ifIndex lists)"`

### Task B2: Plugin resource routes `path_instances/<app>` & `path_ifindexes/<app>`

**Why:** `pkg/plugin/resource.go:22` maps `path_interfaces/<app>` → `/apps/<app>/path` and forwards **full hop objects** verbatim — unusable as a flat PromQL variable value list.

**Files:** Modify `plugin/promhash-datasource/pkg/plugin/resource.go`; Test `plugin/promhash-datasource/pkg/plugin/resource_test.go`.

- [ ] **Step 1: Write failing tests** `TestCallResourcePathInstances` / `TestCallResourcePathIfIndexes`: httptest backend serving `{"instances":["a","b"],"ifIndexes":["1","2"]}`; assert the upstream path requested is `/apps/<app>/selectors` (capture `r.URL.Path`) and the response body unmarshals to a flat `[]string` equal to the selected field.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Add a `sendSelectorField(ctx, sender, app, field string)` helper that GETs `/apps/<escape(app)>/selectors`, decodes into `struct{ Instances, IfIndexes []string }`, picks the field, `json.Marshal`s the flat `[]string`, and sends 200 (or upstream status on error). Add cases:
```go
case strings.HasPrefix(req.Path, "path_instances/"):
    return d.sendSelectorField(ctx, sender, strings.TrimPrefix(req.Path, "path_instances/"), "instances")
case strings.HasPrefix(req.Path, "path_ifindexes/"):
    return d.sendSelectorField(ctx, sender, strings.TrimPrefix(req.Path, "path_ifindexes/"), "ifIndexes")
```
Keep `apps`. Decide whether to keep `path_interfaces/` for back-compat or drop it (README references it → update in B4).
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "feat(plugin): flat path_instances/path_ifindexes resource routes"`

### Task B3: Plugin `CustomVariableSupport` + variable query editor

**Why:** `datasource.ts` extends `DataSourceWithBackend`, adds an unused `apps()` helper, implements neither `metricFindQuery()` nor `VariableSupport` — users cannot create the documented `apps` / `path_interfaces/$app` template variables. `@grafana/data` 10.4.19 exports `CustomVariableSupport`.

**Files:** Modify `plugin/promhash-datasource/src/datasource.ts`, `src/types.ts`, `src/module.ts`; Create `src/VariableQueryEditor.tsx`.

⚠️ **Test-harness gap:** the plugin has **no JS test runner** (package.json has only a webpack `build` script). A real behavioral TS test requires adding jest + ts-jest + @testing-library/react (or vitest). Until then the enforceable gate is `npx tsc --noEmit` plus the Go-side `resource_test`/`server_test` behavioral assertions. **Decide:** add a JS harness (scope creep, ~half a day) or gate TS changes on `tsc --noEmit` for now. The steps below assume the harness is added; if not, replace Step 1/5 test runs with `npx tsc --noEmit`.

- [ ] **Step 1 (if harness): Write failing test.** Mock `getResource` → `['payments','ledger']` for `'apps'` and `['10.0.0.1','10.0.0.2']` for `'path_instances/payments'`. Assert `ds.metricFindValues({varType:'apps'})` returns `MetricFindValue[]` with those texts; assert `ds.metricFindValues({varType:'instances', app:'$myapp'})` (with a templateSrv replacing `$myapp`→`payments`) hit `path_instances/payments`; assert `ds.variables instanceof CustomVariableSupport`.
- [ ] **Step 2: `types.ts`** — add `PromhashVariableQuery extends DataQuery { varType: 'apps'|'instances'|'ifindexes'; app?: string }`.
- [ ] **Step 3: `datasource.ts`** — add resource helpers `listApps()`, `listInstances(app)` (`getResource('path_instances/'+encodeURIComponent(app))`), `listIfIndexes(app)`; add `metricFindValues(q)` resolving `q.app` via `getTemplateSrv().replace(...)`; construct `this.variables = new PromhashVariableSupport(this)` in the ctor; define `PromhashVariableSupport extends CustomVariableSupport<DataSource, PromhashVariableQuery>` with `editor = VariableQueryEditor` and a `query()` that wraps `metricFindValues` in an `Observable` returning a single string field. (Full skeleton in the verified fix design — `from(this.ds.metricFindValues(q)).pipe(map(values => ({data:[{fields:[{name:'value',type:'string',values:values.map(v=>v.text)}],length:values.length}]})))`.)
- [ ] **Step 4: Create `src/VariableQueryEditor.tsx`** — a `Select` for `varType` (`Applications`/`Path instances`/`Path ifIndexes`) plus an `app` `Input` shown for instances/ifindexes; calls `props.onChange` + `props.onRunQuery`.
- [ ] **Step 5: Run `npx tsc --noEmit`** (and the jest test if harness added); confirm compiles/passes.
- [ ] **Step 6: Commit.** `git commit -m "feat(plugin): CustomVariableSupport for apps/instances/ifindexes variables"`

### Task B4: Point the READMEs at the two real variables

- [ ] Update `plugin/promhash-datasource/README.md` "Variable queries" and `README.md` §335–346/373 so the zero-cardinality section uses `path_instances/$app` → `$instance` and `path_ifindexes/$app` → `$ifIndex`, matching the PromQL at `README.md:343` (`instance=~"$instance", ifIndex=~"$ifIndex"`). Remove/replace the `path_interfaces` references. **Commit.** `git commit -m "docs: wire zero-cardinality variables in README"`

---

# Workstream C — api: canonicalization + one honest impact surface (P0-4)

**OPT-14 is the keystone:** `/impact` and `/interface-apps` run the identical `InterfaceImpact` query. Collapse them into one shared lookup helper first, then make that helper the single place that validates input (LOW-empty-params), canonicalizes raw→stored phash (P0-4), and serializes with the wrapped empty-marker form (LOW-iface-null). Doing the leaf fixes independently would duplicate logic across two handlers.

**Files (workstream):** Modify `internal/api/server.go`, `cmd/promhash-api/main.go`, `internal/graph/repo.go` (interface only); reuse `internal/catalog/resolver.go`. Test `internal/api/server_test.go`.

### Task C1: Collapse `/impact` + `/interface-apps` onto a shared handler (OPT-14, LOW-iface-null)

- [ ] **Step 1: Failing test.** `TestImpactAndIfaceAppsIdentical`: for the same query, assert both routes return **byte-identical** wrapped bodies; and `fakeRepo.InterfaceImpact` returning `nil` → body is the wrapped `{"impact":[],"note":"no path known"}` (never literal `null`).
- [ ] **Step 2: Run; confirm fail** (`/interface-apps` currently `writeJSON(rows)` → `null` on nil, no note).
- [ ] **Step 3: Extract** `func (s *Server) lookupImpact(ctx context.Context, device, ifName string, t time.Time) ([]graph.ImpactRow, string, error)` (returns rows + resolved phash + err) and have both routes call a shared response writer using the wrapped form with the empty marker. Register `/interface-apps` as an **alias** to the same handler (keeps the plugin working — see Decision 3).
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "refactor(api): single impact handler, /interface-apps as alias"`

### Task C2: Canonicalize raw `device`/`ifName` via the catalog Resolver (P0-4)

**Why:** handlers hash **raw** params (`phash.Hash(KindIface, device, ifName)`) but the catalog stores interfaces under the **canonical** ifName (`Te0/1/2` → `tengige0/1/2`). Natural names never match → silent empty result. The loader already resolves correctly via `catalog.NewResolver(...).Resolve(device, ifName)`.

- [ ] **Step 1: Failing test.** Extend `fakeRepo` to implement `ListAllInterfaces` returning one interface whose canonical IfName is `tengige0/1/2` (Device `rtr-core-1`, with the matching PHash). Make `InterfaceImpact` record the phash it receives. `GET /impact?device=rtr-core-1&ifName=Te0/1/2` → assert `InterfaceImpact` was called with the **canonical** phash and returns 200 with the app. `ifName=tengige0/1/2` hits the same phash. Unknown ifName → 404 with a JSON body containing the suggestion list.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Add `ListAllInterfaces(ctx) ([]graph.Iface, error)` to the api `Repo` interface (`*graph.Repo` already satisfies it; grow `fakeRepo`). In `lookupImpact`, replace the raw hash with: `ifaces,_ := s.repo.ListAllInterfaces(ctx); res := catalog.NewResolver(ifaces); ifc, err := res.Resolve(device, ifName)`; on `*catalog.NoMatchError` → 404 echoing suggestions, on ambiguous → 409/400, else use `ifc.PHash`. (Per-request resolver rebuild is acceptable for v1 — caching is OPT-10/backlog.)
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(api): resolve raw device/ifName to canonical phash via catalog resolver"`

### Task C3: Reject empty `device`/`ifName` (LOW-empty-params)

- [ ] **Step 1: Failing test.** `GET /impact?device=&ifName=Gi0` and `?device=rtr&ifName=` → 400, and `ListAllInterfaces`/`InterfaceImpact` **never called** (flag in fakeRepo).
- [ ] **Step 2–4:** In `lookupImpact`, before resolution: `if strings.TrimSpace(device)=="" || strings.TrimSpace(ifName)=="" { http.Error(w,"device and ifName are required",400); return }`. Run; PASS; **commit.**

### Task C5: Bound the `at` parameter (LOW-at-bounds)

(Shared `at()` is used by appPath, impact, ifaceApps, and the new appSelectors — fix once.)

- [ ] **Step 1: Failing table test.** `at=-1` and `at=99999999999999` → 400; valid recent ts → 200; absent → now/200; Repo not queried on rejected cases.
- [ ] **Step 2–4:** In `at()`, after `ParseInt`, reject `sec < 0 || sec > 4102444800` (year 2100) → return `time.Time{}, false`. Document the range. Run; PASS; **commit.** `git commit -m "fix(api): bound at parameter and require non-empty device/ifName"`

> `main.go` (`cmd/promhash-api`) change: pass `*graph.Repo` (already does) — the interface just grew. No wiring change beyond the interface addition.

---

# Workstream D — declare/loader temporal integrity (P1)

**P1-atomic is the keystone:** it introduces a session-scoped managed write (`ExecuteWrite`) and tx-taking helpers; **P1-now's** monotonic-validFrom bump must read prior `max(validFrom)` *inside that same transaction*; **P1-retract + LOW-glob** must be safe (never mass-retract on an empty/errored dir or a failed batch).

**Files (workstream):** Modify `cmd/promhash-loader/main.go`, `internal/declare/load.go`, `internal/declare/types.go`, `internal/declare/validate.go`, `internal/graph/repo.go`. Tests: integration (`//go:build integration`, testcontainers) for repo/load behavior; unit for parse/validate.

### Task D1: Atomic reload — close+upsert in one managed transaction (P1-atomic)

**Why:** every repo write is its own auto-committed `ExecuteQuery`; a reload is `CloseAppValidity` (3 writes) + `UpsertDeclaredApp` (1) = 4 separate commits. A crash mid-load leaves edges closed with nothing recreated → app vanishes from current state.

- [ ] **Step 1: Failing integration test.** `TestReloadIsAtomic`: Load rev1 at t1; `ReloadDeclaredApp` for rev2 → `AppPath(now)` returns exactly rev2's hops. Then a failure-injection reload that errors after the close statements → assert `AppPath(now)` STILL returns rev1's hops (rollback left old edges open).
- [ ] **Step 2: Run; confirm fail / no such method.**
- [ ] **Step 3: Implement.** Add `func (r *Repo) execWrite(ctx, fn func(tx neo4j.ManagedTransaction) error) error` opening a write session + `ExecuteWrite`. Refactor `CloseAppValidity` and `UpsertDeclaredApp` into tx-taking helpers `closeAppValidityTx(tx, appPHash, at)` (keep the **three separate `tx.Run` statements** — preserve the existing "re-MATCH independently" comments verbatim) and `upsertDeclaredAppTx(tx, d)`. Add `func (r *Repo) ReloadDeclaredApp(ctx, d DeclaredApp, at time.Time) error` = `execWrite(close then upsert)`. Keep the public `CloseAppValidity`/`UpsertDeclaredApp` as thin `execWrite` wrappers (still used by tests + the retract pass). Change `load.go:39-42` to call `ReloadDeclaredApp`.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(graph): atomic close+upsert reload via managed transaction"`

### Task D2: Strictly-increasing `validFrom` (P1-now + LOW-validfrom-commit)

**Why:** one wall-clock `now` is both the prior revision's `validTo` and the new revision's `validFrom`. Two loads in the same second → `[T,T)` zero-width window that no point-in-time query can hit → that revision is silently un-queryable.

- [ ] **Step 1: Failing integration test.** `TestSameSecondReloadKeepsQueryableWindow`: Load rev1 then rev2 with the **same** timestamp T. Assert `AppPath` at the rev1 window still returns rev1's hops; `AppPath(now)` returns only rev2; rev2's stored `validFrom` is strictly greater than rev1's.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Inside the reload tx (D1), read `max(do.validFrom)` for the app; if requested `validFrom <= prev`, bump to `prev+1` (second). Use the bumped value for **both** the close `at` and the new `validFrom` (contiguous + strictly increasing). Add a `-commit-time` flag (RFC3339) / `GIT_COMMIT_TIME` env to `main.go` supplying the base `validFrom` (falls back to `time.Now().UTC()`); CI sets it from `git show -s --format=%cI`.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(loader): monotonic validFrom from commit time, bump same-second collisions"`

### Task D3: Tombstone removed declarations (P1-retract) + glob guard (LOW-glob)

**Why:** the loader only processes files *present* on disk; deleting a declaration YAML never retracts the app — it stays `validTo IS NULL` forever.

- [ ] **Step 1: Failing integration test.** Seed+Load `payments` at t1; reconcile with a present-set **not** containing `payments` at t2 → `AppPath(now)` returns 0 hops, but `AppPath(t1+1m)` still returns history. Negative: present-set **containing** `payments` leaves it unchanged. Guard: a batch with one failed file does NOT tombstone the still-valid app.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Add `func (r *Repo) ListOpenDeclaredApps(ctx) ([]string, error)` → `MATCH (app:Application)-[:RUNS_AS]->(:ApplicationService)-[do:DEPENDS_ON]->() WHERE do.validTo IS NULL AND do.provenance='declared' RETURN DISTINCT app.phash`. In `main.go`: build `present` set of `phash.Hash(KindApp, a.App)` **only from files that parsed AND validated**; run the reconcile pass **only when `!validateOnly`, `!failed`, and the dir was non-empty**; for each open phash not in `present`, `CloseAppValidity(ctx, phash, bumpedNow)`. Also: capture the `filepath.Glob` error (don't `_`); treat zero matches as `os.Exit(1)` unless a new `-allow-empty` flag is set (reconcile is a real "all removed" scenario only behind that gate).
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "feat(loader): reconcile/tombstone removed declarations, guard empty dir"`

### Task D4: Strict YAML decode (P1-strict) + reject path-less deps (LOW-dep-nopath)

- [ ] **Step 1: Failing unit tests.** `Parse` with unknown field `runz_as:` → error naming the field; a dep with `paht:` typo → error; existing `sample` + `pathSugarSample` (both `path:` and `paths:`) → no error; empty bytes → clear error. `Validate` with a dep having neither `path` nor `paths` → error; `paths:[{hops:[]}]` → error.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** `Parse`: switch to `dec := yaml.NewDecoder(bytes.NewReader(b)); dec.KnownFields(true); dec.Decode(&a)` with EOF→"empty declaration". `Validate`: inside the `dep` loop add `if len(dep.Candidates())==0 { errs = append(errs, ...) }` and per-candidate `if len(p.Hops)==0 { errs = append(errs, ...) }`.
- [ ] **Step 4: Run; PASS.** ⚠️ Before merge, run the loader's strict parse over the repo's actual `declared/` dir — strict mode turns previously-silently-wrong files into hard failures (that's the intent, but confirm none of the existing valid declarations use extra keys). **Commit.** `git commit -m "feat(declare): strict YAML decode, reject path-less dependencies"`

---

# Workstream E — graph temporal invariant + schema (P1)

### Task E1: `DEPENDS_ON` append-only (P1-dependson) — **ship this standalone first; tiny, zero read-path risk**

**Why:** `MERGE (svc)-[do:DEPENDS_ON]->(target)` has no temporal discriminator, so on reload it re-matches the edge `CloseAppValidity` just closed and reopens it — overwriting `validFrom` and destroying the historical interval. Asymmetric with `TAKES`, which puts `validFrom` in the MERGE pattern. Latent (no served query reads `DEPENDS_ON`) but the invariant is violated.

- [ ] **Step 1: Failing integration test** `TestDependsOnAppendOnlyAcrossReloads`: Load at t1; `CloseAppValidity(app,t2)` + Load at t2; query all `DEPENDS_ON` edges svc→target. Assert exactly **2** edges: t1 edge has `validFrom=t1, validTo=t2` (interval preserved); t2 edge has `validFrom=t2, validTo=null`. Idempotency: a second load at the same t2 keeps count at 2.
- [ ] **Step 2: Run; confirm fail** (current code returns one edge `validFrom=t2, validTo=null`).
- [ ] **Step 3: Implement.** Change `repo.go:168-170` to mirror `TAKES`:
```cypher
MERGE (svc)-[do:DEPENDS_ON {provenance:'declared', source:$source, validFrom:$validFrom}]->(target)
  SET do.confidence=$confidence, do.observedAt=$observedAt, do.validTo=null
```
- [ ] **Step 4: Run; PASS. Commit + open PR immediately** (independent of everything else). `git commit -m "fix(graph): make DEPENDS_ON append-only (validFrom in MERGE pattern)"`

### Task E2: Remove the phantom `criticality` field (P1-criticality) — Decision 4 = remove

- [ ] **Step 1: Failing test.** Drive `/impact` through `api.NewServer` with a fakeRepo returning a populated `ImpactRow`; decode the response into `map[string]any` and assert `_, ok := row["criticality"]; ok == false`.
- [ ] **Step 2: Run; confirm fail** (key present, always `""`).
- [ ] **Step 3: Implement.** Delete `Criticality` from `ImpactRow` (`model.go:54`); drop `, coalesce(a.criticality,'') AS criticality` from `repo.go:301`; remove from the `ImpactRow` literal (`repo.go:310-311`); fix the doc comment (`repo.go:292`); remove `"criticality":"tier-1"` from `README.md:317`. ⚠️ grep `plugin/` TS for `criticality` first — if the datasource reads it, update there too.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix: remove phantom criticality field never populated anywhere"`

> If Decision 4 flips to *populate*, this becomes a larger task crossing servicenow + seed + graph; design is in the verified fix notes. Coordinate with OPT-12 (BL) to define ImpactRow once.

### Task E3: `app_name_unique` constraint (OPT-6) — backs a live query

**Why:** `ListApps` (`ORDER BY name`) and `AppServiceName` (`{name:$app}`) do label scans; only `phash` is constrained.

- [ ] **Step 1: Failing integration test.** After `EnsureConstraints`, `SHOW CONSTRAINTS` contains `app_name_unique` on `Application.name`; creating a second `Application` with a duplicate name errors.
- [ ] **Step 2–3: Implement.** In `EnsureConstraints`, after the phash loop: `r.write(ctx, "CREATE CONSTRAINT app_name_unique IF NOT EXISTS FOR (a:Application) REQUIRE a.name IS UNIQUE", nil)`. ⚠️ If duplicate display names are possible in real data, fall back to a plain `CREATE INDEX app_name ...`. Audit existing data first.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "perf(graph): app_name_unique constraint backing ListApps/AppServiceName"`

> OPT-7 (composite `(device,ifName)` index) and OPT-8 (relationship range indexes) have **no current consumer** — defer to backlog (BL-3); adding them now is dead index-maintenance cost.

---

# Workstream F — catalog/phash identity (P1, the cheap fixes)

**LOW-keysafe is the keystone:** it defines the single normalization rule (case-fold + internal-whitespace collapse + NFC + `keySafe` `:`/control rejection). P1-device-identity then applies that *identical* rule at all three identity sites or the split/merge bug reappears.

**Files (workstream):** `internal/phash/phash.go`, `internal/catalog/canon.go`, `internal/catalog/sync.go`, `internal/catalog/resolver.go`, `internal/declare/validate.go`. Tests: catalog unit + one integration round-trip.

### Task F1: Single normalization rule + apply `keySafe` to device (LOW-keysafe)

- [ ] **Step 1: Failing tests.** `CanonicalIfName("cisco","Gi 0/3") == CanonicalIfName("cisco","Gi0/3")` (internal whitespace collapsed); `keySafe("device","a:b")` errors; a full-width/NBSP variant normalizes to ASCII (two inputs resolve to one interface).
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Introduce one shared normalizer (e.g. `catalog.CanonicalDevice` / an exported phash normalizer) that trims, collapses internal whitespace, applies NFC (`golang.org/x/text/unicode/norm`), case-folds, and runs a `keySafe` that rejects `:`/control chars. Apply it consistently in canon + device paths. Extend `validate.go` to check non-empty `h.If` and run keySafe on `device`.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(catalog): single normalization rule, keySafe on device/ifName"`

### Task F2: Consistent device identity at hash/store/lookup (P1-device-identity)

**Why:** `phash.Hash` lowercases device, but `sync.go` stores `Device` **verbatim** and `resolver.go` keys `byDevice` on the **exact** stored string. `Rtr1` and `rtr1` silently merge into one node, yet `Resolve("rtr1",...)` misses a node stored as `Rtr1`.

- [ ] **Step 1: Failing test.** Build a resolver from an Iface with normalized Device; assert `Resolve("Rtr1",ref)`, `Resolve("rtr1",ref)`, `Resolve(" RTR1 ",ref)` all return the same PHash, nil error.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** In `Sync`, normalize device before building the `Iface` (`Device: dev`) **and** computing `ifacePHash(dev, canon)`. In `NewResolver`, key on the normalized device (defensively, for legacy data). In `Resolve`, normalize the incoming device before `byDevice` lookup. Same transform everywhere.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(catalog): normalize device identity at hash, store, and resolve"`

### Task F3: Reject empty interface refs (P1-emptyref)

- [ ] **Step 1: Failing tests.** Sync two rows whose ifName+ifDescr both canonicalize to `""` → they do NOT collapse to one node (Sync skips/errors). `Resolve(device,"")` → `NoMatchError`, not a silent bind.
- [ ] **Step 2–3: Implement.** In `Sync`, `if canon=="" { skip-with-warn (preferred) or error }`. In `Resolve`, reject empty `ref` up front; guard match arms so empty `want`/`refLower` never match `IfAlias`/`MetricIfName`.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(catalog): reject empty canonical interface names at sync and resolve"`

### Task F4: Arista `Et` + abbrev cleanup (LOW-abbrev) and unknown-device message (LOW-nomatch)

- [ ] **LOW-abbrev:** add `{"et","ethernet"}` to the abbrev table; assert `CanonicalIfName("arista","Et1")==...("Eth1")==...("Ethernet1")=="ethernet1"` and a `Resolve(device,"Et1")` finds an `Ethernet1`-harvested interface. Remove the dead self-mapping entries; fix the wrong "longest-prefix-first" comment to describe exact-match-on-leading-alpha-token. Add only confirmed-needed vendor abbreviations (don't gold-plate).
- [ ] **LOW-nomatch:** distinguish "unknown device" from "known device, no match" — when `byDevice[dev]` is absent, `Error()` should say "no interfaces known for this device" instead of a dangling "did you mean: ". Depends on F2 (normalized membership check).
- [ ] **Commit.** `git commit -m "fix(catalog): canonicalize Arista Et, fix abbrev comment, clearer unknown-device error"`

> **Defer to BL-2:** P1-rename, LOW-validity (node-level Interface validity), LOW-dedup — these are large, interdependent, and gated on the identity-model decision (Decision 2).

---

# Workstream G — http client robustness (P2)

**Shared GET helper supersedes piecemeal fixes:** LOW-errbody + LOW-retry both want one helper on the nautobot/servicenow clients (set auth header, Do, check status, read body). Build it once; route pagination + retry + error-body through it. Size the P2-timeout deadline **last**, after pagination/retry, so a legitimate multi-page crawl isn't aborted.

⚠️ **Correction:** review.md implies all clients lack timeouts. In fact nautobot/servicenow already set `http.Client{Timeout: 30s}`; **only `promclient` lacks a client timeout** and `promapi.Config` exposes a `Client *http.Client` field to inject one.

**Files:** `internal/promclient/prom.go`, `internal/nautobot/nautobot.go`, `internal/servicenow/servicenow.go`, `cmd/promhash-catalog/main.go`, `cmd/promhash-seed/main.go`, `internal/graph/repo.go` (observedAt read-back); new `internal/httpx` (retry helper).

### Task G1: promclient timeout + per-call context deadlines (P2-timeout)

- [ ] **Step 1: Failing test.** httptest handler that blocks before writing the body; `HarvestInterfaces` with `context.WithTimeout(ctx,50ms)` returns within a few hundred ms with `errors.Is(err, context.DeadlineExceeded)` (does not hang).
- [ ] **Step 2–3:** In `promclient.New`, build `&http.Client{Timeout: 30*time.Second}` and pass `promapi.Config{Address: addr, Client: httpClient}` (Client and RoundTripper are mutually exclusive — use Client only). In `cmd/promhash-catalog/main.go` and `cmd/promhash-seed/main.go`, wrap each upstream call in `ctx, cancel := context.WithTimeout(base, 60*time.Second)` + immediate `cancel()` (not deferred in a loop). Add a `-timeout` flag (default 60s) to the catalog binary.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(promclient): client timeout + per-call deadlines on harvest/seed"`

### Task G2: Pagination for ServiceNow + Nautobot (P2-pagination)

- [ ] **Step 1: Failing tests.** ServiceNow: httptest returns a full page at `sysparm_offset=0`, a partial page at `offset=pageSize`; assert `Applications()` returns the **union** and that the request carries `sysparm_limit`/`sysparm_offset`. Nautobot: first response has `next:"<url>"`, second `next:null`; assert `DeviceInstanceMap` merges both pages and issued the second request; guard test: an always-`next` server eventually errors (page cap), not infinite loop.
- [ ] **Step 2–3:** ServiceNow: loop with `sysparm_limit=<pageSize>&sysparm_offset=<n>` (preserve existing query via `url.Values`), stop on a short page (or honor `X-Total-Count`/`Link rel="next"`), respect `ctx`, cap pages. Nautobot: add `Next string json:"next"` to the envelope, replace `?limit=0` with `?limit=1000`, follow `dl.Next` until empty, re-sending the auth header each hop. ⚠️ Validate `next`'s host matches the configured host before re-issuing (avoid token leakage to an attacker-controlled redirect).
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(clients): paginate ServiceNow and Nautobot fully"`

### Task G3: Shared retry/backoff (LOW-retry) + error-body surfacing (LOW-errbody) + harvest error handling (LOW-harvest-swallow)

- [ ] **LOW-errbody:** on non-2xx, read `io.LimitReader(resp.Body, 4<<10)` and include it in the error; assert the error contains the upstream body text ("boom") + status. (Do this first — the helper both retry and pagination reuse.)
- [ ] **LOW-retry:** add `internal/httpx.DoWithRetry(ctx, hc, newReq, attempts, base)` — retries on transport errors + 429/500/502/503/504, honors `Retry-After`, exponential backoff + jitter, aborts on ctx cancel, attempts=3. Wrap each nautobot/servicenow page request and the prom `Query` (retry on non-ctx errors). Test: 503-then-200 → success with expected hit count; 400/404 → single hit; ctx-cancel mid-backoff → prompt return.
- [ ] **LOW-harvest-swallow:** in `HarvestInterfaces`, non-vector result type → `fmt.Errorf("unexpected result type %T (want vector)", val)` (empty *vector* stays empty, not an error); non-numeric `ifIndex` → skip row + count, log skipped count; surface `model.Warnings`. Test: matrix result → error; one good + one `ifIndex="abc"` → len 1 + skipped count.
- [ ] **Commit.** `git commit -m "fix(clients): retry/backoff, surface error bodies, fail on non-vector harvest"`

### Task G4: Read `observedAt` back (P2-staleness, cheap prerequisite only)

- [ ] **Step 1: Failing test.** Upsert an Iface with `ObservedAt=time.Unix(1700000000,0).UTC()`; `GetInterfaceByPHash` returns that ObservedAt (currently zero).
- [ ] **Step 2–3:** In `ifaceFromProps` add `if v,ok := p["observedAt"].(int64); ok { out.ObservedAt = time.Unix(v,0).UTC() }`. Log oldest `observedAt` + age at end of catalog sync.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "fix(graph): read observedAt back; log catalog age"`

> The full P2-staleness metric (`promhash_catalog_age_seconds` via textfile/Pushgateway) + retire policy is larger and destructive — backlog.

---

# Workstream H — api self-observability (P2-obs)

Depends on Workstream C (shared handler) landing first.

**Files:** `internal/api/server.go`, `internal/graph/repo.go`, `cmd/promhash-api/main.go`.

### Task H1: health/ready/metrics + middleware chain

- [ ] **Step 1: Failing tests.** `/healthz`→200; `/readyz` with `fakeRepo.Ping` nil→200, err→503; a panicking handler → 500 + server survives a subsequent request; deadline middleware → handler observes `context.DeadlineExceeded`; `/metrics`→200 prometheus exposition with a known metric name.
- [ ] **Step 2: Run; confirm fail.**
- [ ] **Step 3: Implement.** Add `Ping(ctx) error` to the api `Repo` interface (`*graph.Repo` wraps `drv.VerifyConnectivity`); register `GET /healthz` (static), `GET /readyz` (Ping→503 on error), `GET /metrics` (`promhttp.Handler()` — `prometheus/client_golang` is already a dep). Add `func (s *Server) Handler() http.Handler` wrapping the mux with ordered middleware `recover → logging → deadline → metrics`; `main.go` uses `.Handler()` instead of `.Mux()` (keep `.Mux()` for tests, or migrate them) and keeps the existing `http.Server` timeouts. Make the per-request DB deadline a `-db-timeout` flag (generous default, e.g. 10s) so slow point-in-time queries aren't truncated.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "feat(api): healthz/readyz/metrics, recover/logging/deadline middleware"`

---

# Workstream I — ci / build / docs hygiene (P2 + LOW)

Mostly independent; two coupling points: LOW-fatal and LOW-seed-constraints both edit `cmd/promhash-seed/main.go` (do together); the env-var fallback helper benefits both P2-secrets and LOW-fatal.

### Task I1: README secrets via env (P2-secrets) — docs only

- [ ] Rewrite the README Quickstart (≈ lines 134–173): rename `NEOPASS`→`NEO4J_PASS`, add `export SERVICENOW_PASS=...` / `export NAUTOBOT_TOKEN=...`, and **remove** `-neo4j-pass`/`-servicenow-pass`/`-nautobot-token` from all invocations (the tools already read those env vars when the flag is empty — verified in all 5 mains). Add a CI grep guard that fails if the Quickstart block contains those secret flags. **Commit.** `git commit -m "docs: pass secrets via env, not CLI flags; add CI guard"`

### Task I2: `run() error` refactor across all 5 mains (LOW-fatal) + seed constraints check (LOW-seed-constraints)

- [ ] **Step 1: Failing test (promhash-api).** Extract `run(ctx, deps) error`; start it, cancel ctx → returns nil and the (recorder) driver's `Close` was called once. Listen-failure path (bind an in-use addr) → run() returns non-nil **and** Close still called.
- [ ] **Step 2–3:** Convert each `main()` to `func main(){ if err:=run(); err!=nil { log.Printf(...); os.Exit(1) } }`; replace `log.Fatal` inside run() with `return err`. For promhash-api: route `ListenAndServe` error out via `errCh` + `select` with `<-ctx.Done()`; use a fresh non-cancelled `context.WithTimeout(context.Background(),5s)` for the deferred `drv.Close`. Extract `flagOrEnv(flagVal, envKey string) string` and unit-test the flag-wins-over-env precedence. Change `cmd/promhash-seed/main.go:40` from `_ = r.EnsureConstraints(ctx)` to `if err := r.EnsureConstraints(ctx); err != nil { return fmt.Errorf("ensure constraints: %w", err) }`.
- [ ] **Step 4: Run; PASS. Commit.** `git commit -m "refactor(cmd): run() pattern so deferred Close runs; seed checks EnsureConstraints"`

### Task I3: CI action SHA pins (LOW-ci-pins) + Makefile `ci` target (LOW-makefile)

- [ ] **LOW-ci-pins:** pin `actions/checkout@v4` / `actions/setup-go@v5` to full commit SHAs (resolve via `gh api repos/actions/checkout/git/refs/tags/v4` — **don't guess**) with `# v4.x.y` comments. Add a lint/meta-test asserting no `uses:` line references a bare `@v<major>`.
- [ ] **LOW-makefile:** add `.PHONY` `ci build-plugin lint-plugin test-plugin`; plugin targets `cd plugin/promhash-datasource && go build/vet/test ./...`; combined `ci: build lint test build-plugin lint-plugin test-plugin`. Verify by introducing a temp compile error in the plugin module and asserting `make ci` fails.
- [ ] **Commit.** `git commit -m "ci: pin actions to SHAs; make ci covers plugin module"`

---

# Backlog — decision-gated / large / speculative

Each has a verified design in the workflow output; do **not** start until the owning decision is made. None block the product (Workstreams A–C) or the integrity fixes (D–F).

| ID | Item | Why deferred | Decision owner |
|----|------|--------------|----------------|
| **BL-1** | **OPT-3** mapping-metric + `group_left` model | Architectural; supersedes A's internals. Ship A first. | Decision 1 |
| **BL-2** | **P1-rename + LOW-validity + LOW-dedup** (interface rename/decommission) | Large, interdependent; node-level Interface validity + `(device,ifIndex)` continuity. Conflicts with the "make `(device,ifIndex)` the primary key" alternative. | Decision 2 |
| **BL-3** | **OPT-9** archive closed subgraphs **vs OPT-8** relationship indexes | Either/or; both forward-looking, current fan-out tiny. | Decision 5 |
| | **OPT-10** TTL cache for `ListApps` in the api process | Optimization; staleness trade-off; do as a caching decorator (keep `Repo` stateless). | — |
| | **OPT-12** potential-vs-active impact (provenance/confidence/candidateCount on `ImpactRow`) | Changes `/impact` JSON + plugin; design `ImpactRow` once with E2/criticality. | with OPT-13 |
| | **OPT-13** preserve candidate-path identity in `AppPath` (`candidate`/`pathId`) | Changes `Hop` JSON + plugin + enrich grouping. | — |
| | **OPT-7** composite `(device,ifName)` index | No current consumer — add only when a query MATCHes on those props. | — |
| | **OPT-15** pagination/filtering on `/apps` | Pre-scale; bare-array contract vs wrapped response (plugin coupling). | — |
| | **P2-validate-only** catalog-snapshot validation | Decouples PR gate from Neo4j availability; needs snapshot refresh cadence. | — |
| | **P2-staleness** full metric + retire policy | Destructive retire; metric delivery (textfile/Pushgateway) for a one-shot CLI. Read-back shipped in G4. | — |

---

## Self-review (against `review.md`)

- **Coverage:** all 49 findings map to a task or a flagged backlog item. P0 (1–4) → A1/A2 (P0-1,P0-2), B (P0-3), C2 (P0-4). P1 (10) → A1 (enrich-dedup), D (retract/atomic/now/strict), E1/E2 (dependson/criticality), F2/F3 (device-identity/emptyref); P1-rename → BL-2. P2 → C5/H/G/I + BL. All OPT/LOW placed.
- **Corrections folded in:** `/apps/{app}/selectors` must be **created** (B1); only promclient lacks a client timeout (G1); `/interface-apps` is consumed by the plugin so alias not delete (C1).
- **Type/signature consistency:** `FederationMatch` → `[]string` and `enrich.Selectors` (raw values) are introduced in A2/B1 and reused consistently; the api `Repo` interface grows by `ListAllInterfaces` (C2) + `Ping` (H1) — batch the `fakeRepo` updates. `ReloadDeclaredApp`/`execWrite` (D1) are referenced by D2/D3.
- **No placeholders:** every task names exact files, the concrete change, and a behavioral assertion. Where a step says "full skeleton in the verified fix design," the code is in the workflow output transcript — transcribe it verbatim when executing.
- **Known soft spot:** P2/OPT tasks are task-level rather than 5-micro-step TDD; their fix designs are concrete enough to execute, but the engineer must still write the failing behavioral test first per the universal rule.

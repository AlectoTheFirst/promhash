# promhash Merged Implementation Plan — bug-fixes + observability reshape

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 49 verified code bugs from `review.md` **and** fully incorporate the design/operating-model recommendations from `review-observability.md`, having committed to **reshaping the metric data plane** (replace per-app federation/tenant-Prometheus with a single shared rule-evaluator + one `remote_write` + a bounded mapping series).

**Architecture:** Go monorepo (`github.com/AlectoTheFirst/promhash`) + separate Grafana plugin module (`github.com/AlectoTheFirst/promhash-datasource` under `plugin/`), coupled over HTTP. Neo4j graph store. The projection layer changes from *N per-app tenant Prometheis pulling `/federate`* to *one shared rule-evaluator that joins a bounded `promhash_interface_app{…}=1` mapping series against raw counters with `group_left`, writing once via `remote_write` with `app` as a write-relabel external label*. Three serving tiers: **(0)** free graph lookup, **(1)** bounded mapping series + `group_left` (the new primary path-health layer), **(2)** optional full per-app projection from the same shared evaluator.

**Tech Stack:** Go 1.26, neo4j-go-driver/v5, prometheus/client_golang (api + promhttp + push), prometheus/common/expfmt, gopkg.in/yaml.v3, TypeScript + `@grafana/data`/`@grafana/runtime` 10.4.19, React.

**Verification basis:** All findings re-read against commit `ef77e44`. The two reviews + this reshape were designed by two multi-agent workflows that read the actual code; every claim below carries a `file:line` in the source designs. **Universal test rule (the reviews' core lesson):** existing tests assert generated-string equality against goldens and therefore pass despite the P0 bugs. **Every new/changed test asserts behavior** — PromQL join results, label-set intersection, dedup, resolved identity, HTTP status, ctx cancellation, graph traversal counts, metric values — never a golden string.

---

## Status vs. the prior plan

This merged plan supersedes [2026-06-02-promhash-review-fixes.md](2026-06-02-promhash-review-fixes.md) (the "prior plan") **in part**:

- **Retained verbatim** (full TDD detail lives in the prior plan; only deltas noted here): Workstreams **C** (api canonicalization + single impact surface), **D** (declare/loader temporal integrity), **E1** (DEPENDS_ON append-only), **E3/OPT-6** (`app_name_unique`), **F** (catalog/phash identity), **G** (http clients + observedAt read-back), **H** (api health/metrics middleware), **I** (ci/build/docs hygiene).
- **Superseded / reshaped** → new workstreams here: prior **Workstream A** → **RA** (projection reshape); prior **Workstream B** → **RB** (composite-key dashboard); prior **Task E2** (remove criticality) → **RC** (populate + RBAC-gate); prior backlog **OPT-3/BL-1, OPT-12, P2-staleness** → elevated into **RA / RC / RE / RF**.
- **New greenfield** (no analog in the prior plan): **RD** (Alertmanager enrichment), **RE** (drift detector + catalog tombstone), **RF** (self-observability across all binaries + scheduling/packaging), **RG** (README reframe), **RH** (RBAC), **RI** (device-node / DEPENDS_ON exposure / topology overlays / multi-region — roadmap).

---

## The two maintainer decisions (locked)

1. **Full incorporation** of `review-observability.md` — greenfield design recommendations become real workstreams (RD, RE, RF, RG, RH/RI), not just notes.
2. **Commit to the reshape** — replace per-app federation/tenant-Prometheus with one shared rule-evaluator + one `remote_write` (`app` as write-relabel external label) + the bounded mapping-series tier (RA). The prior plan's P0-2 cross-product federation fix is **not done** — federation is removed, so P0-1/P0-2 *vanish* with it.

### Resulting reconciliations (what flips, what's superseded)

| Item | Prior plan | Merged decision | Owner |
|------|-----------|-----------------|-------|
| Enrich data plane | Fix federation in place (A1/A2) | **Replace** with mapping series + `group_left` + shared evaluator | RA |
| `job=` matcher (P0-1), cross-product (P0-2) | Fix | **Moot** — no federation; rules join on stable `(instance,ifName)` | RA |
| `ifOperStatus` | Drop (OPT-4) | **Consume** → `app:if_oper_up:state` series (+ `ifHighSpeed` capacity) | RA |
| Zero-card dashboard | Two flat lists → `$instance`×`$ifIndex` (still a cross-product!) | **Composite `(instance,ifName)` selectors / `group_left`**; ship reference dashboards | RB |
| `criticality` | **Remove** (E2 / Decision 4) | **Populate** (ServiceNow `business_criticality` + declaration override), **gated behind RoleBusiness** redaction | RC + RH |
| Potential-vs-active impact + staleness | Backlog (OPT-12) | **Elevated to P1**: `provenance/confidence/candidateCount/potential/redundancy` + `mappingValidFrom/ObservedAt` on `ImpactRow`; surfaced in `/impact` & `AppPath` | RC |
| Catalog staleness metric + retire | Backlog (P2-staleness) | **Elevated**: reversible node-validity tombstone w/ grace window + drift detector + textfile metric | RE |
| Self-observability | api-only (H) | **All 5 binaries** + `last_success_timestamp` + shipped CronJob/timer + catalog→enrich ordering guard | RF |
| Alert enrichment | — | **New day-1 deliverable**: Alertmanager webhook → `/impact` → owner/customer/criticality + "potential (declared)" | RD |
| RBAC | — | **New P1**: bearer-token + RoleBusiness redaction of customer/owner/criticality + query audit | RH |
| README positioning | small edits in A2/B4 | **Reframe**: lead with impact/provenance/business-context; three-tier model; chokepoint-first; preconditions/limitations | RG |

---

## Master sequencing (dependency DAG)

```
PHASE 0 — substrate (mostly from prior plan, unchanged):
  E1 (DEPENDS_ON append-only) ........ tiny, standalone, ship first
  C  (api canonicalization + single impact handler: C1/C2/C3/C5)
  F  (catalog/phash identity: F1→F2→F3→F4)
  G  (http clients: G1 timeout, G2 pagination, G3 retry/errbody/harvest, G4 observedAt read-back)
  I  (ci/build: I1 secrets+guard, I2 run() refactor, I3 ci pins/Makefile)
  D  (loader temporal: D1 atomic → D2 monotonic → D3 retract+glob; D4 strict YAML)
  E3 (app_name_unique)

PHASE 1 — product restoration (the reshape):
  RA (projection reshape) ............ RA0 helpers → RA1 mapping series → RA2 group_left rules
                                       → RA3 capacity/status harvest → RA4 shared-evaluator config
                                       → RA5 rewire enrich → RA6 composite selectors → RA7 docs
  RB (composite dashboard + plugin) .. RB1 composite Selectors → RB2 /selectors → RB3 routes
                                       → RB4 alertable frame → RB5 QueryEditor → RB6 variables
                                       → RB7 free dashboard → RB8 curated dashboard (needs RA)
                                       → RB9 README PromQL
  RC (honest impact) ................. RC1 criticality(seed) → RC2 criticality(decl) → RC3 ImpactRow
                                       → RC4 AppPath staleness → RC5 API surfacing(needs C1)
                                       → RC6 plugin frame → RC7 docs

PHASE 2 — operability & governance:
  H  (api health/metrics) → RF (extend to all binaries; RF7 prune needs RA+D3)
  RH (RBAC: RH-1a authn → RH-1b redaction[needs C1] → RH-1c audit)
  RD (alert enrichment) .............. needs C + RC + RH; RD8 split-brain doc needs RA
  RE (drift + tombstone) ............. needs G4 + D3 + G1; RE-B node-validity → RE-A replay → RE-C metric

PHASE 3 — positioning & roadmap:
  RG (README reframe) ................ honest sections early; tier copy AFTER RA; secrets shared w/ I1
  RH-target / RI (device node RI-2, DEPENDS_ON exposure RI-3, topology overlays RI-4, multi-region RI-5)
```

**Recommended merge order:** `E1 → C → (F, G, I in parallel) → D → E3 → RA → RB → RC → H → RF → RH → RD → RE → RG → RI`.

**Open decision still required:** **Interface identity model (BL-2)** — keep `(device, canonicalIfName)` vs make `(device, ifIndex)` primary. RA/RB lean on the stable `(instance, ifName)` join key (works under either), but **RE's rename detection, RH/RI-5's multi-region key, and F's identity fixes all rewrite `ifacePHash`** — settling BL-2 first avoids repeated identity migrations. Recommendation unchanged from prior plan: ship F's cheap consistency fixes now; decide the primary-key question before RE-A and RI-5.

---

# PHASE 0 — Substrate (from the prior plan)

These workstreams are **unchanged**; full bite-sized TDD steps are in [the prior plan](2026-06-02-promhash-review-fixes.md). Execute them as written there, with these **deltas only**:

- **Workstream C** — unchanged. RA, RC, RD, RH, RI all build on C1's single `lookupImpact` handler and C2's server-side canonicalization. Do **not** skip C1's `/interface-apps`-as-alias decision (the plugin calls it).
- **Workstream D** — unchanged. D3 adds `ListOpenDeclaredApps`, reused by **RE** (drift replay enumeration) and **RF7** (prune cross-check); keep the name or expose both.
- **Workstream E** — **E1** (DEPENDS_ON append-only) ship first/standalone; it is the foundation **RI-3** (transitive dependency exposure) builds on. **E3/OPT-6** (`app_name_unique`) unchanged. **E2 is REPLACED by RC** (populate criticality) — do not remove the field. OPT-7/OPT-8 stay deferred (BL-3).
- **Workstream F** — unchanged. F1/F2 normalization is a prerequisite for **RE**'s rename detection (else false positives) and for **RI-5**'s scrape-domain key.
- **Workstream G** — unchanged. **G1** (promclient timeout) must precede **RE-A1** (InterfaceLiveness adds N upstream calls). **G4** (observedAt read-back) is a hard prerequisite for **RE** and **RF4** (CatalogAge).
- **Workstream H** — unchanged, but **RF extends it to all binaries** and **RH inserts authn+audit into its `Server.Handler()` middleware chain** (`recover → audit → authn → logging → deadline → metrics`); `/healthz`,`/readyz`,`/metrics` must be on the authn skip list.
- **Workstream I** — unchanged. **I1** (secrets→env + CI grep guard) shares the README Quickstart fence with **RG5**; **I2** (run() refactor) merges with **RF2**.

---

# PHASE 1 — Product restoration (the reshape)

## Workstream RA — reshape the projection layer (P0)

**Replaces prior Workstream A entirely.** The per-hop federation+tenant model is deleted; the bounded mapping series + `group_left` becomes the primary path-health serving layer, evaluated in one shared rule-evaluator that `remote_write`s once with `app` as a write-relabel external label. This obviates P0-1 (no `honor_labels`/`job=`) and P0-2 (no `/federate` selector). `ifOperStatus` is consumed (not dropped); `ifHighSpeed` capacity is added.

**Three tiers:** **T0** free graph lookup → Grafana variables (reuses `enrich.Selectors`); **T1** bounded `promhash_interface_app{…}=1` mapping series + `group_left` recording rules (primary); **T2** optional full per-app rollups from the same shared evaluator (no per-app Prometheus).

**Join key:** prefer stable **`(instance, ifName)`** where `snmp_exporter` exposes `ifName` on `ifHC*Octets` (confirmed in this estate: `promclient/prom.go:37` groups by `ifName`); fall back to a composite `iface="instance:ifIndex"` synthesized by a `metric_relabel_config` in the shared evaluator. Gate via `-join-key=ifname|composite` (default composite — always available). Document the `snmp_exporter` ifName precondition.

**Files:** new `internal/enrich/{mapping.go,pathhealth.go,evaluator.go}` + tests + `testdata/{mapping.golden.prom,path-health.rules.golden.yaml,evaluator.golden.yaml}`; modify `internal/enrich/{rules.go,federation.go,tenant.go}`, `cmd/promhash-enrich/main.go`, `internal/promclient/prom.go`, `internal/graph/model.go`, `internal/api/server.go`; docs `README.md` §352-362, new `docs/deploy/shared-evaluator.md` (retires `federation-tenant.md`).

- [ ] **RA0 — Factor shared helpers.** Extract `enrich.Selectors`, `labelValueEscape(s)` (Prometheus label-value escaping: `\`, `"`, newline), and `sortPoints(...)`. Keep `FederationMatch`/`RuleGroup` calling them so legacy goldens stay byte-identical (transitional substrate). **Test:** `Selectors` dedups + numerically sorts ifIndexes (`[9,42,100]` not lexical); `labelValueEscape` round-trips `a"b\c` through an expfmt-style parse.
- [ ] **RA1 — Mapping-series generator (T1a).** `internal/enrich/mapping.go`: `type JoinKey int (JoinByIfName|JoinByComposite)`; `type MappingPoint{Instance,Device,IfName,Iface,App,Service,Direction string; IfIndex int}`; `MappingSeries(app,service string, hops []graph.Hop, jk JoinKey) []MappingPoint` (expand transit→ingress+egress, dedup by `(instance,ifIndex,app,direction)`, `iface=instance+":"+itoa(ifIndex)`); `RenderMappingSeries(points) string` emitting sorted `promhash_interface_app{app,service,device,ifName,instance,ifIndex,iface,direction} 1` lines with escaped values. **Test:** a transit hop in two candidate paths at different `seq` → exactly **2** points (1 ingress, 1 egress), not 4; `RenderMappingSeries` output parses via `expfmt.TextParser`, value==1, and a shared core link used by 2 apps yields **2 series differing only in `app`** (the `group_left` fan-out precondition); two devices both ifIndex 42 with different instances → distinct `iface`.
- [ ] **RA2 — `group_left` path-health rules (T1b).** `internal/enrich/pathhealth.go`: `PathHealthRules(jk JoinKey) string` emitting one static, app-independent rule group `promhash_path_health`: egress/ingress octet-rate rules `rate(ifHC{Out,In}Octets[5m]) * on(<keys>) group_left(app,service,device,ifName) promhash_interface_app{direction="egress|ingress"}`; capacity `app:if_capacity_bps = ifHighSpeed*1e6 * on(<keys>) group_left(...) …`; status `app:if_oper_up:state = ifOperStatus == bool 1 * on(<keys>) …`; util `app:if_util:ratio = (app:if_egress_octets:rate5m*8)/app:if_capacity_bps`; rollups `app:path_util_max:ratio = max by(app,service)(…)`, `app:path_oper_up_min:state = min by(app,service)(…)`, `app:path_hops_down:count = count by(app,service)(oper_up==bool 0)`. `<keys> = on(iface)` (composite) or `on(instance,ifName)` (ifname). **Test (PromQL behavioral, requires `promql.Engine`/`promqltest` harness — net-new infra, ~½ day):** load fixture `ifHCOutOctets` for `iface 10.0.0.1:7` over 5m + a `promhash_interface_app{app="payments",direction="egress"} 1`; evaluate the egress rule → resulting series carries `app/service/device/ifName` + nonzero rate. An iface with **no** mapping series → **no** output series (join drops unmapped). Shared-link → 2 output series (app A, B). Rollup: hops util 0.3 & 0.8 → `app:path_util_max:ratio == 0.8` (max, never sum/avg).
- [ ] **RA3 — Harvest capacity + status.** `promclient/prom.go`: `type CapRow{Instance string; IfIndex int; SpeedMbps, OperStatus float64}`; `CapacityStatus(ctx) ([]CapRow,error)` running `max by(instance,ifIndex)(ifHighSpeed)` + `max by(instance,ifIndex)(ifOperStatus)`. Consumed at query time by the evaluator, not stored in graph. **Test:** httptest Prometheus returns both vectors → one `CapRow` per `(instance,ifIndex)`; an iface present in ifHighSpeed but absent in ifOperStatus still returns a row (OperStatus zero flagged).
- [ ] **RA4 — Shared-evaluator config.** `internal/enrich/evaluator.go`: `type EvaluatorOpts{ScrapeTarget,RemoteWriteURL,TenantLabel string; JoinKey JoinKey}`; `SharedEvaluatorConfig(opts) string` rendering ONE Prometheus-agent `scrape_configs` block (target = ScrapeTarget), `metric_relabel_configs` synthesizing `iface` from `[instance,ifIndex]` sep `:` in composite mode, `rule_files:[path-health.rules.yaml]`, `global.external_labels:{tenant:<TenantLabel>}`, and ONE `remote_write{url}`. No per-app job, no `honor_labels`. **Test:** `yaml.Unmarshal` → exactly one scrape_config + one remote_write, `rule_files` contains the rules file, composite mode has a `metric_relabel_configs` with `target_label==iface`/`separator==":"`; assert no `job_name` matching `promhash-fed-` and `honor_labels` absent.
- [ ] **RA5 — Rewire `cmd/promhash-enrich`.** Collect `AppPath` hops for ALL `-apps`, fold into one `[]MappingPoint`, write three SHARED artifacts to `outDir/_shared/`: `mapping.prom`, `path-health.rules.yaml`, `evaluator.yaml`. New flags `-join-key`, `-remote-write-url`, `-tenant-label`, `-prune-legacy`. Stop writing per-app `federate.match`/`rules.yaml`. Factor `writeSharedArtifacts(dir string, apps map[string][]graph.Hop, opts EvaluatorOpts) error`. **Test:** two apps sharing one core hop → all three `_shared/` files exist; `mapping.prom` parses with 2 series for the shared hop; `path-health.rules.yaml` unmarshals to group `promhash_path_health`; NO `outDir/<app>/federate.match` written.
- [ ] **RA6 — Composite selectors for the corrected dashboard.** (Converges with RB.) Extend `/apps/{app}/selectors` (RB2) to also return composite `iface` pairs; add `enrich.IfaceSelectors(hops) []string` returning OR-of-exact `instance:ifIndex` pairs. **Test:** hops `{(10.0.0.1,42),(10.0.0.2,43)}` → `ifaces==["10.0.0.1:42","10.0.0.2:43"]`, and `(10.0.0.1,43)`/`(10.0.0.2,42)` are NOT in the set (exactly 2 entries — no cross-product).
- [ ] **RA7 — Docs reshape.** Rewrite README §352-362 + new `docs/deploy/shared-evaluator.md`: three-tier model, single shared evaluator + one remote_write, join-key decision + `snmp_exporter` ifName precondition, the `group_left` PromQL, capacity/status/rollup series, and the **as-of-emit LTS-attribution caveat** (graph and LTS disagree on history once `app`-labeled samples are immutable). **Test:** CI guard parses README PromQL fences and asserts the path-health example uses `group_left(` + `promhash_interface_app{` and the dashboard example uses `iface=~"$iface"`, **not** `instance=~"$instance", ifIndex=~"$ifIndex"`.

**Effort:** Large. RA1/RA2 medium (unblock the primary serving layer); RA3/RA6/RA7 small; RA4/RA5 medium. Net-new: a `promqltest`/`promql.Engine` fixture harness for RA2 (~½ day).
**Risk:** (1) Composite join needs the `metric_relabel` synthesizing `iface` on raw counters — ship it inside `SharedEvaluatorConfig` so it travels with the rules; RA2's negative test asserts unmapped ifaces drop out. (2) `ifname` join needs stable `ifName` on octet series (not universal) — default composite. (3) Asymmetric routing: per-direction mapping records the reverse flow on a wrong `direction` — documented. (4) `ifHighSpeed` can be 0/wrong → guard util with `>0`. (5) LTS attribution is as-of-emit, non-retractable — doc only.
**Interim option (flagged, not a task):** RA is larger than the prior in-place fix, so the product emits no curated data until RA lands. If a faster stopgap is needed, the prior plan's A1 "drop `job=`" (1 day) restores the *old* federation path temporarily — but it's throwaway under the reshape. Recommend going straight to RA unless a hard deadline forces the stopgap.

## Workstream RB — composite-key dashboard + alertable plugin frame (P0)

**Reshapes prior Workstream B.** The prior B returned two flat lists feeding `$instance`×`$ifIndex` — itself the cross-product the obs review flags. RB makes the composite `(instance, ifName)` the unit, ships reference dashboards for both tiers, and makes the plugin frame an actual alert target.

**Files:** `internal/enrich/federation.go`, `internal/api/server.go`, `plugin/.../pkg/plugin/{resource.go,query.go}`, `plugin/.../src/{datasource.ts,types.ts,QueryEditor.tsx,VariableQueryEditor.tsx,module.ts}`, new `gitops/grafana/dashboards/promhash-path-health-{free,curated}.json`, `README.md`.

- [ ] **RB1 — Composite `Selectors`.** `type IfaceSelector{Instance,IfName string; IfIndex int; Direction string}`; `Selectors(hops) []IfaceSelector` = one per unique `(instance,ifName)` (NOT `(instance,ifIndex)`), sorted, never nil; `IfName` is raw `MetricIfName`. `FederationMatch` derives from it (keep its match[] output unchanged for any transitional consumer). **Test:** hops sharing ifName `Te0/1/2` with distinct ifIndex → the `(Instance,IfName)` PAIR set equals exactly the declared pairs (dups collapsed), and no entry crosses `10.0.0.5↔ifIndex 42`.
- [ ] **RB2 — `/apps/{app}/selectors` returns composite interfaces.** Handler returns `{"interfaces":[{instance,ifName,ifIndex,direction}…],"instances":[…],"ifIndexes":[…],"ifaces":[…]}` (flat lists kept for back-compat; `ifaces` = RA6 composite pairs). Reuse bounded `at()` (C5). Empty path → `interfaces:[]` (never null). **Test:** the two payments hops → `interfaces` has exactly the 2 real tuples; reconstructing `instance×ifIndex` would give 4 combos but `interfaces` yields 2 (proves no cross-product).
- [ ] **RB3 — Plugin resource routes.** Replace `path_interfaces/<app>` with `path_ifaces/<app>` (flat `instance|ifName` tokens), `path_instances/<app>`, `path_ifnames/<app>`; keep `apps`. **Test:** upstream path requested is `/apps/<app>/selectors`; `path_ifaces/payments` body is a flat `[]string` of `10.0.0.1|Te0/1/2`, `10.0.0.5|Te0/1/2` — each token binds an instance to ITS ifName.
- [ ] **RB4 — Alertable `app_path` frame.** `query.go` app_path branch: add a `time` field (`q.TimeRange.To` repeated per hop) + numeric `value` field (hop confidence) + `instance`/`direction` fields; frame meta `PreferredVisualization=table`. **Test:** frame has ≥1 `FieldTypeTime` AND ≥1 `FieldTypeFloat64`, and `instance`/`direction` fields exist with len == hop count.
- [ ] **RB5 — QueryEditor device/ifName + non-silent default.** Show device+ifName inputs when `queryType==='impact'`; default `queryType='app_path'` on first render; `query.go` `default:` → return `ErrDataResponse(StatusBadRequest,"unknown queryType")` instead of silently running impact. **Test:** `QueryType=""` → response error `StatusBadRequest` and impact upstream NOT called; `QueryType=impact` with device/ifName → upstream `/interface-apps` carries them.
- [ ] **RB6 — CustomVariableSupport.** `datasource.ts`: `metricFindValues(q)` resolving `q.app` via `getTemplateSrv().replace`, `this.variables = new PromhashVariableSupport(this)` (`CustomVariableSupport` from `@grafana/data` 10.4.19); varType ∈ {apps,ifaces,instances,ifnames}; `VariableQueryEditor.tsx`. **Test gate:** `npx tsc --noEmit` (no JS runner in repo; see RG note) + Go-side RB3 proves the resource shape. If jest/vitest added: mock `getResource`→ composite tokens, assert `metricFindValues({varType:'ifaces',app:'$a'})` hits `path_ifaces/payments`.
- [ ] **RB7 — Free-tier reference dashboard JSON.** `promhash-path-health-free.json`: `$app` (Query→apps), `$iface` (Query→`path_ifaces/$app`, multi-value), one Prometheus panel that **repeats by `$iface`** pinning ONE exact `{instance="…",ifName="…"}` per repeat, plus the alertable `app_path` panel. **No** `instance=~ AND ifIndex=~` anywhere. **Test:** parse JSON; assert no target has both `instance=~"$instance"` and `ifIndex=~"$ifIndex"`; assert a target pins exact `instance=`+`ifName=` from one `$iface` token.
- [ ] **RB8 — Curated-tier reference dashboard JSON** (needs RA mapping series). `promhash-path-health-curated.json`: single `$app` variable, zero instance/ifIndex variables; panels use `rate(ifHCOutOctets[5m]) * on(instance,ifName) group_left(app,service,direction) promhash_interface_app{app="$app",direction=~"egress|transit"}` + the `ifOperStatus` down-hop query. **Test:** no variable named instance/ifIndex/ifName (only app); every target uses `on(instance,ifName) group_left(` + `promhash_interface_app{app="$app"`; no `ifIndex` in any join key.
- [ ] **RB9 — README PromQL rewrite.** Delete the cross-product `rate(ifHCOutOctets{instance=~"$instance", ifIndex=~"$ifIndex"}[5m])`; document the free-tier composite `path_ifaces/$app` + per-pair-repeat pattern + the alertable `app_path` panel; add a curated § with the `group_left` query and the **whole-link-total-not-app-share** caveat. **Test:** CI grep guard fails if README contains `ifIndex=~"$ifIndex"`; asserts `path_ifaces/$app` + `group_left(` present.

**Effort:** Medium (~4-5 eng-days excl. RA dependency). RB1-RB7,RB9 sit on the critical path and don't need RA; RB8 is blocked on RA's mapping series.
**Risk:** the per-pair-repeat free-tier UX is more cumbersome than the broken single query — mitigated by shipping the alertable `app_path` panel as the authoritative free view + the CI guard. `ifname` join assumes `snmp_exporter` ifName hygiene; `ifIndex` kept as fallback (also correct since `instance` pins the device).

## Workstream RC — honest impact: potential-vs-active, staleness, populate criticality (P1)

**Replaces prior Task E2 (which removed criticality).** Designs `ImpactRow` once as the coherent shape; surfaces provenance/confidence/candidateCount/potential/redundancy + mapping age; populates `criticality` from ServiceNow `business_criticality` with declaration override. **Criticality (and owner/customer) are RoleBusiness-gated by RH-1b** — populated but not exposed to every caller.

**Files:** `internal/graph/{model.go,repo.go}`, `internal/api/server.go`, `internal/servicenow/servicenow.go`, `cmd/promhash-seed/main.go`, `internal/declare/{types.go,load.go,validate.go}`, `plugin/.../pkg/plugin/query.go`, `README.md` + tests.

- [ ] **RC1 — Populate criticality (seed path).** `servicenow.go`: add `Criticality string` + `json:"business_criticality"`. `UpsertAppSeed` gains trailing `criticality string`; Cypher `SET a.criticality=CASE WHEN $criticality<>'' THEN $criticality ELSE a.criticality END` (empty never wipes). `cmd/promhash-seed/main.go` passes `a.Criticality`. **Test:** ServiceNow httptest with `"business_criticality":"tier-1"` → `Applications()[0].Criticality=="tier-1"`; integration: `UpsertAppSeed(...,"tier-1")` then `(...,"")` → criticality stays `tier-1`.
- [ ] **RC2 — Declaration override.** `declare.App`: `Criticality string yaml:"criticality"`; `DeclaredApp.Criticality`; `UpsertDeclaredApp` SET with same CASE guard; `load.go` threads it; `validate.go` keySafe when non-empty. **Test:** seed `tier-3`, then load declaration `criticality: tier-1` → graph has `tier-1`; load without criticality → stays `tier-1`.
- [ ] **RC3 — `ImpactRow` reshape + `InterfaceImpact` Cypher.** Extend `ImpactRow` with `Provenance, Confidence float64, CandidateCount int, Potential bool, Redundancy string, MappingValidFrom int64, MappingObservedAt int64`. Rewrite `InterfaceImpact` with a `CALL{}`-scoped `candidateCount` (count of valid candidate Paths under the Connection — **constant across the customer fan-out, does not row-multiply**) + `min(t.validFrom)/min(t.observedAt)/head(collect(t.provenance))/max(t.confidence)`; derive `Potential=(provenance!="flow")`, `Redundancy=candidateCount<=1?"sole-path":"1-of-N"`. **Test (integration):** an iface on 1 of 2 ECMP candidates → `CandidateCount==2, Redundancy=="1-of-N", Potential==true, Provenance=="declared", Confidence==1.0`; a sole-path app → `CandidateCount==1`; an app with a customer → row count == #customers (NOT ×candidateCount).
- [ ] **RC4 — Surface mapping age in `AppPath`/`Hop`.** Add `Hop.MappingValidFrom/MappingObservedAt int64`; `AppPath` RETURN gains `t.validFrom/t.observedAt`. **Test:** load at `validFrom=T` → every Hop's MappingValidFrom==`T.Unix()`.
- [ ] **RC5 — API surfacing** (needs C1). Thread the reshaped `ImpactRow` through the shared `lookupImpact` handler; add top-level `impactKind = "active (flow)"` if all rows flow else `"potential (declared)"`; `/interface-apps` (alias) returns the same wrapped shape. **Test:** fakeRepo row `{Provenance:"declared",CandidateCount:2,Criticality:"tier-1"}` → body `impactKind=="potential (declared)"`, `impact[0].candidateCount==2`, `redundancy=="1-of-N"`, `potential==true`; all-flow rows → `impactKind=="active (flow)"`; `/interface-apps` byte-identical to `/impact`.
- [ ] **RC6 — Plugin impact frame.** `query.go` default case: decode the wrapped body; add `owner/criticality/potential(bool)/confidence(float64)/candidateCount(int64)` fields (numeric `candidateCount` gives Grafana an alert target). **Test:** frame has those fields; `candidateCount` is `FieldTypeInt64` value 2; `potential` true. (Customer/owner/criticality respect RH-1b redaction at the API.)
- [ ] **RC7 — README/contract.** Replace the `/impact` example with the reshaped body; fix the stale `"confidence":0` → 1; document criticality sourced from ServiceNow + declaration override; add "potential (declared) until flow." **Test:** doc guard asserts the `/impact` JSON block parses and contains `impactKind`/`candidateCount`/`potential`.

**Effort:** Medium (~3 days). **Risk:** `CALL{}` aggregation needs Neo4j 5.x (testcontainers 5.23 validates); `head(collect(provenance))` is order-nondeterministic for mixed declared+flow (v1 all declared; prefer "flow wins" later). Populating criticality means `/impact` returns business tier — **handled by RH-1b redaction**, which must land for customer-attributed routes.

---

# PHASE 2 — Operability & governance

## Workstream RH — RBAC + query audit (P1, governance gate)

**New.** Bearer-token authn + a RoleBusiness boundary that gates customer-attributed fields (owner/customer/criticality) + a query audit log. Blocks RD (privileged customer-reading caller) and any chargeback roadmap.

**Files:** new `internal/api/{auth.go,audit.go}`, `internal/api/server.go`, `cmd/promhash-api/main.go`.

- [ ] **RH-1a — Authn middleware.** `auth.go`: `Role{viewer,business}`, `Principal`, `TokenStore`, `StaticTokenStore` (`sha256(token)→Principal` from `PROMHASH_TOKENS_FILE`, `subtle.ConstantTimeCompare`), `Authn(ts, audit)` reading `Authorization: Bearer`, skip-prefix `{/healthz,/readyz,/metrics}`, `PrincipalFrom(ctx)`. Wire into `Server.Handler()` chain after H1. **Test:** `/impact` no header → 401; unknown token → 401; valid viewer → 200; `/healthz` no token → 200 (skip honored).
- [ ] **RH-1b — Role-gated redaction.** In the shared `lookupImpact` response writer (C1): if `Role != business`, blank `Customer/Owner/Criticality` and add top-level `"redacted":["customer","owner","criticality"]`. Redact in Go, not Cypher (one query). **Test:** viewer token → row `customer==""`/`owner==""`/`criticality==""` + `redacted` present; business token → `customer=="acme"`; `/interface-apps` applies the SAME redaction.
- [ ] **RH-1c — Query audit (JSONL).** `audit.go`: `AuditEvent{Time,Subject,Role,Method,Path,Query(device/ifName/at only),Status,RowCount,CustomerExposed}`, `AuditJSONL(io.Writer)`, middleware after authn with a status-capturing ResponseWriter. `CustomerExposed = role==business && any row Customer!=""`. **Test:** business `/impact` returning a customer row → JSONL line has `CustomerExposed==true`, `Status==200`, `RowCount==1`, and NO token text; viewer → `CustomerExposed==false`; a 401 still logs one line with empty Subject.

**Effort:** Medium. **Risk:** redaction-in-Go risks a future route forgetting — funnel ALL customer output through the single `lookupImpact` writer + a test that every impact-shaped route redacts. Static token store is v1; OIDC/JWT + per-customer scoping is the **documented target** (roadmap), and chargeback/customer-echo are blocked on it.

## Workstream RF — self-observability + scheduling/packaging (P1)

**Extends prior Workstream H to all 5 binaries.** Adds a success/age signal to the four one-shot CLIs, ships CronJob/timer manifests with a catalog→enrich ordering guard, and a post-RA decommission/prune flow. **Merges with prior I2 (run() refactor).**

**Files:** new `internal/obs/job.go`, `cmd/promhash-{catalog,enrich,loader,seed,api}/main.go`, `internal/api/server.go`, `internal/graph/repo.go`, new `deploy/kubernetes/*.yaml` + `deploy/systemd/*` + `deploy/DECOMMISSION.md`, `README.md`, `docs/deploy/federation-tenant.md`.

- [ ] **RF1 — `internal/obs` job telemetry.** `JobMetrics{NewJobMetrics(job), Start, Observe(kind,n), Fail, Finish(ctx,ok)}` registering `promhash_job_last_success_timestamp_seconds`/`_last_start_timestamp_seconds`/`_duration_seconds`/`promhash_job_processed_total{kind}`/`promhash_job_failure_total` on a private registry with const label `job`. `Finish` delivers by `PROMHASH_METRICS_MODE` (textfile=atomic tmp+rename OpenMetrics via expfmt; pushgateway=`push.New(...).Gatherer(reg).Push()`; off=no-op). On `ok=false`, leave success UNCHANGED (age climbs). Delivery errors logged, never fatal. **Test:** Observe→Finish(true) in textfile mode → parse `.prom`, `processed_total{kind}` correct + success ts ~now; Finish(false) → `failure_total==1`, success ts unchanged; mode=off → no file.
- [ ] **RF2 — Wire 4 CLIs + run() refactor (merges I2).** `main(){m:=NewJobMetrics;m.Start();err:=run(ctx,m,deps);_=m.Finish(ctx,err==nil);if err!=nil{...os.Exit(1)}}`; replace `log.Fatal` inside `run()` with `return err` (deferred `drv.Close` fires); `m.Observe` per binary; `m.Fail()` where `failed=true`. **Test:** catalog run() with recorder Repo → returns nil, `Close` called, `.prom` shows `processed_total{kind="interfaces"}==3` + fresh success; failure path → non-nil, `Close` STILL called, `failure_total==1`, success ts not advanced.
- [ ] **RF3 — api `build_info` + shared registry** (on top of H1). Register `promhash_build_info{version,commit}=1`; surface obs registry via promhttp. Add `Repo.Ping(ctx)` if H1 didn't. **Test:** `/metrics` body has `promhash_build_info` value 1 with injected labels; `/readyz` 200/503 on Ping nil/err.
- [ ] **RF4 — catalog→enrich ordering guard.** `Repo.CatalogAge(ctx)` = `MATCH (i:Interface) RETURN max(i.observedAt)` (reuses G4); flag `-require-catalog-fresh <dur>` on enrich → if stale, return error and write NO artifacts. **Test (integration):** interfaces at now-20m/now-5m; `-require-catalog-fresh=10m` → error + no artifacts; `=30m` → artifacts written.
- [ ] **RF5 — Kubernetes CronJobs + PrometheusRule.** `cronjob-catalog.yaml` (`*/5`, concurrencyPolicy Forbid), `cronjob-enrich.yaml`/`cronjob-loader.yaml` each with an initContainer that curls `promhash_job_last_success_timestamp_seconds{job="promhash-catalog"}` and exits non-zero unless `now-value < PROMHASH_CATALOG_MAX_AGE` (default 900). `recording-rules-promhash-jobs.yaml`: alerts `time()-success>1800` (catalog stale) + `failure_total>0`. **Test:** `kubeconform`/`--dry-run=client` passes; enrich initContainer references the catalog success metric; `promtool test rules` → stale alert fires at 31m, not at 5m.
- [ ] **RF6 — systemd timers + textfile delivery.** `promhash-{catalog,loader}.{service,.timer}`, `promhash-enrich.service` with an `ExecStartPre` freshness check on the textfile `.prom` age; units set `PROMHASH_METRICS_MODE=textfile`. **Test:** `systemd-analyze verify` passes; enrich service has the ExecStartPre ordering guard.
- [ ] **RF7 — `enrich -prune` decommission** (needs RA + D3). After writing current `-apps` artifacts, `os.RemoveAll` each `gitops/enrichment/<app>/` whose app is off-allowlist AND not in `ListOpenDeclaredApps` (genuinely retracted), and drop its mapping-series row; `processed_total{kind="pruned"}`. `deploy/DECOMMISSION.md`: retract YAML → `enrich -prune` → no per-app teardown post-RA → LTS samples immutable. **Test:** dirs `{payments,ledger}`, allowlist `payments`, `ListOpenDeclaredApps→{payments}` → `ledger/` removed, `payments/` kept, `pruned==1`; negative: `ListOpenDeclaredApps→{payments,ledger}` → `ledger/` NOT removed.

**Effort:** Medium-large. **Risk:** metric delivery is env-coupled → default mode off, Finish never fails the job. Ordering guard too-tight → generous default + skippable. RF7 `os.RemoveAll` is destructive → double gate (off-allowlist AND retracted) + negative test; ship only after RA + D3.

## Workstream RD — Alertmanager enrichment recipe (P1, the day-1 deliverable)

**New.** A supported webhook receiver: interface-down alert → `/impact` → owner/customer/criticality + "potential (declared)" + sole-path-vs-1-of-N into annotations/routing. Most accuracy-tolerant use case.

**Depends on:** C (server-side canonicalization + always-wrapped body), RC (potential/candidateCount/criticality fields), RH (bearer auth). Degrades gracefully without RC (annotations show "redundancy unknown").

**Files:** new `cmd/promhash-alert-webhook/main.go`, `internal/alertenrich/{enrich.go,client.go,annotations.go}` + tests, new `docs/deploy/alerting.md`, `README.md`, `Makefile`, `.github/workflows`.

- [ ] **RD1 — `LabelMap.Extract`.** `AMAlert`/`AMPayload` (Alertmanager webhook v4); `LabelMap{Device,IfName []string}` (defaults Device=`[device,instance,node,hostname]`, IfName=`[ifName,ifDescr,ifAlias,interface,ifIndex]`); `Extract(labels)→(device,ifName,ok)` first-non-empty per ordered group. **Test:** `{device,ifName}`→ok; `{instance,ifIndex}` fallback→ok; `{alertname}`→ok=false; precedence: device wins over instance.
- [ ] **RD2 — `/impact` Bearer client.** `ImpactClient.Impact(ctx,device,ifName,at)` GET with `Authorization: Bearer apiKey`, decodes wrapped `{interface,impact[],note}`, returns body+status; tolerates absent RC fields (zero-value defaults). **Test:** request carries the Bearer header + raw device/ifName query; wrapped body decodes; `note:"no path known"`→0 apps; 401→status 401 + non-nil error.
- [ ] **RD3 — `Enrich` orchestration.** Extract→if !ok set Reason `no-device/no-ifname` (never calls client); else `/impact`; map 404→unresolved, 409/400→ambiguous, empty/note→no-path-known, 401/403→hard error; rows→Matched, Apps sorted by criticality rank then name. **Test (fake client):** missing device → Matched=false, client call-count 0; 2 rows tier-2+tier-1 → Apps[0] tier-1; note→no-path-known; 401→error.
- [ ] **RD4 — Annotations/labels.** `BuildAnnotations(Enrichment)` → `promhash_impacted_apps/owners/customers/criticality/impact_confidence/redundancy/mapping_age/interface/impact_detail`; criticality = highest across apps; `impact_confidence` constant `"potential (declared)"`; redundancy from SolePath/CandidateCount; mapping_age humanized. `BuildRoutingLabels` → `{promhash_owners, promhash_criticality}` for AM label promotion. **Test:** tier-1 app, CandidateCount 3 → `impact_confidence=="potential (declared)"`, redundancy contains "1 of 3 candidates", criticality=="tier-1", owners=="team-pay", mapping_age=="3d"; two apps → highest tier wins; age -1→"unknown".
- [ ] **RD5 — Webhook handler.** `POST /webhook`: require `Authorization: Bearer <PROMHASH_WEBHOOK_KEY>` (constant-time, 401), decode `AMPayload`, enrich each alert, mode=annotate → 200 JSON; bump metrics. **Test:** no auth → 401 + upstream NEVER hit; correct key + firing alert → 200 with `promhash_owners=="team-pay"`; malformed → 400; `enriched_total{result="matched"}` += 1.
- [ ] **RD6 — reinject mode.** Merge routing labels into `alert.Labels` + annotations, re-POST to `-downstream-url`; require it at startup when mode=reinject. **Test:** downstream receives labels `promhash_owners`/`promhash_criticality`; empty `-downstream-url` in reinject → run() error.
- [ ] **RD7 — webhook self-obs.** `/healthz`, `/readyz` (probe `-api-url/healthz`), `/metrics` with `promhash_alert_webhook_{alerts_received_total,enriched_total{result},impact_request_duration_seconds,impact_errors_total{status}}`. **Test:** /healthz 200; /readyz 200/503; after N alerts `alerts_received_total==N`.
- [ ] **RD8 — split-brain alerting doc** (needs RA). `docs/deploy/alerting.md`: the WHERE-each-alert-lives table reconciled to RA's single-evaluator topology (interface health in MAIN Prom → fires the webhook; curated app series in the SHARED evaluator already app-labeled → no webhook; promhash self-health); SNMP-cadence timing asymmetry; accuracy-tolerance framing; working AM `webhook_config` + `route{match_re: promhash_owners}` snippet. README recipe "Enrich alerts with business impact". **Test:** the embedded AM YAML parses; doc references all three domains.

**Effort:** Medium (~1.5-2 days for lib+binary; mirrors `cmd/promhash-api`). **Risk:** over-paging if RC caveats absent → constant "potential (declared)" + visible "redundancy unknown". Auth blast radius (privileged customer-reading caller) → inbound webhook key + RH business-token service credential. Label-mapping fragility → `result="unresolved"` metric, not silent drop.

## Workstream RE — drift detector + catalog tombstone lifecycle (P1)

**New (the obs review's highest-leverage missing piece).** A scheduled job replays every declared path against the live catalog + "carries traffic / ifOperStatus up", emitting `declared_paths_stale_total{reason}`; plus a reversible catalog interface tombstone with a grace window so the resolver stops binding ghosts.

**Depends on:** G4 (observedAt read-back), D3 (`ListOpenDeclaredApps`), G1 (promclient timeout), F (normalization, else false "renamed"). Sequence: **RE-B** (node validity, smaller) → **RE-A** (replay + liveness + binary) → **RE-C** (metric/report).

**Files:** new `cmd/promhash-drift/main.go`, `internal/drift/{replay.go,reason.go}`, `internal/metricsfile/textfile.go`, `internal/promclient/prom.go`, `internal/graph/{repo.go,model.go}`, `internal/catalog/resolver.go`, new `docs/deploy/drift.md`, `README.md` + tests.

- [ ] **RE-B1 — Interface node validity.** `Iface` gains `Stale bool, ValidFrom/ValidTo time.Time`. `UpsertInterface`: `ON CREATE SET n.validFrom=$observedAt`, always `SET n.stale=false,n.validTo=null` (revive). `ifaceFromProps` reads back (builds on G4). Add `MarkInterfaceStale(ctx,phash,at)` (SET stale=true,validTo,staleReason), `ClearInterfaceStale`, `ListInterfacesNotSeenSince(ctx,cutoff)`. **Test (integration):** upsert at T0 → Stale=false,ValidFrom=T0; MarkStale T1 → Stale=true,ValidTo=T1; re-upsert T2 → revived; `ListInterfacesNotSeenSince(T1.5)` returns the old iface.
- [ ] **RE-B2 — Resolver excludes tombstoned.** `NewResolver` skips `i.Stale` (add `NewResolverWithStale(...,true)` for forensic callers); `suggest()` skips stale. **Test:** stale `Gi0/9` → `Resolve` returns NoMatchError and suggestions exclude `Gi0/9` but include live `Gi0/1`; `NewResolverWithStale(true)` → resolves the ghost.
- [ ] **RE-A1 — promclient liveness.** `Liveness{HasTraffic bool; OperUp *bool; Queried bool}`; `InterfaceLiveness(ctx,instance,ifIndex)` two exact-selector instant queries (traffic rate over `-traffic-window`; `ifOperStatus`); empty oper vector → `OperUp=nil`. `InterfaceLivenessBatch` (one round-trip). Client timeout (consumes G1). **Test:** traffic>0 + status=1 → HasTraffic/OperUp true; status absent → OperUp nil (no false down); blocking stub + 50ms ctx → `DeadlineExceeded` quickly; selector is exact (no `=~`).
- [ ] **RE-A2 — `drift.ReplayAll`.** Reason taxonomy `{missing_interface,renamed,no_traffic,oper_down,stale_catalog}`; per hop: re-Resolve (NoMatch→missing; different phash→renamed), stale-catalog (observedAt<graceCutoff||Stale), liveness (HasTraffic false→no_traffic; OperUp!=nil&&!*OperUp→oper_down). One reason/hop by priority `missing>renamed>stale_catalog>oper_down>no_traffic`. **Test (fake repo+liveness+real Resolver):** each reason triggered in isolation; a clean path → zero findings; priority: missing+no_traffic → one finding `missing_interface`.
- [ ] **RE-C1 — textfile metric + JSON report.** `metricsfile.WriteTextfile(path, Report)` → `declared_paths_stale_total{app,reason}`, `declared_paths_checked_total{app}`, `promhash_drift_last_success_timestamp_seconds`, `promhash_drift_oldest_observed_seconds`, atomic tmp+rename. JSON report to `-report-out`. **Test:** build Report → parse `.prom` with expfmt, `declared_paths_stale_total{app="payments",reason="missing_interface"}==2`, success ts >0, no `.tmp` left.
- [ ] **RE-A3 — `cmd/promhash-drift` binary.** Mirror `promhash-catalog`; flags `-prometheus,-neo4j*,-at,-grace,-traffic-window,-textfile-out,-report-out,-fail-on-reason,-annotate`; run() pattern; exit 1 iff a finding matches `-fail-on-reason`. `docs/deploy/drift.md` (cron/CronJob/systemd + node_exporter textfile collector). **Test (injected fakes):** missing finding + `-fail-on-reason=missing_interface` → error/exit; `=renamed` → nil; textfile+JSON files exist and parse.

**Effort:** Medium-large. **Risk:** false `no_traffic` on idle redundant links → lowest priority + excluded from default `-fail-on-reason`. Label-hygiene → `OperUp=nil` degrades safely. Extra Prom load → batch + timeout. `renamed` needs F normalization first. **Reshape-independent:** liveness reads MAIN Prometheus regardless of projection topology.

---

# PHASE 3 — Positioning & roadmap

## Workstream RG — README reframe + honest positioning (P1, docs)

**New.** Lead with impact/provenance/business-context; demote cardinality to "why we don't tag the firehose"; document the three-tier model, the path-health-vs-impact split, chokepoint-first examples, "when NOT to use," and a Preconditions & limitations section. **Owns the README prose** that prior A2-step8/B4 touched.

**Reconciliation:** RG must reflect the **populate-criticality** decision (RC) — i.e. criticality is a real, **RoleBusiness-gated** field (overriding the RG design agent's conservative "keep removed" stance). Tier-1/tier-2 copy must be byte-consistent with RA's emitted series name/expr; land RG's honest sections early, the tier-PromQL sections after RA.

**Files:** `README.md`, `docs/deploy/federation-tenant.md`, `plugin/.../README.md`, `Makefile`, `.github/workflows`.

- [ ] **RG1 — Reframe the lead.** New H1 subtitle (drop "without ever exploding Prometheus cardinality"); new "## What promhash is for" (impact/blast-radius, provenance/temporal, flow-blind-edge business context); demote cardinality to "## Why we don't put an app label on the firehose" that carves out the tier-1 mapping series as NOT forbidden. **Test:** CI structure guard — first H2 after H1 is the impact/provenance section; the cardinality phrase absent from lines 1-12; firehose header follows the lead header.
- [ ] **RG2 — Three-tier model + split table + reshape rewrite.** Insert the path-health-vs-impact table (+ "serving layer not a cheaper system" caveat); rewrite "## How it works" to three tiers; rewrite "## Enrichment and federation" to the shared-evaluator model (delete per-app tenant prose). **Test:** parse table → 2 rows with `group_left` (path-health) / `graph` (impact); tier-1 PromQL fence has `group_left`+`on(instance,ifName)`+`promhash_interface_app` byte-identical to RA's fixture; "tenant Prometheus"/"dedicated Prometheus instance"/"per-app tenant" absent from those sections.
- [ ] **RG3 — Chokepoint-first + when-NOT-to-use + competitive.** Replace foregrounded `rtr-core-1` examples with a policy-pinned chokepoint; demote the multipath example to "Modeling the dynamic core (potential until flow)"; add "## Honest scope" (a few dozen golden paths), "## When NOT to use promhash" (≥4 bullets), "## How promhash compares". **Test:** "When NOT to use" exists with ≥4 items; first end-to-end example references a chokepoint not `rtr-core`; any remaining `rtr-core` example is under a header containing "potential".
- [ ] **RG4 — Preconditions & limitations + phash wording.** Add the section with the grounded caveats: `snmp_exporter` ifName hygiene (REQUIRED), SNMP cadence floor, asymmetric routing, multi-region identity, graph-vs-LTS divergence, phash-is-name-hashing (+ capture/use sys_id), declared-paths-potential, day-1 ROI, no-RBAC-yet (xref RH). Replace README:81 "collapses to one node". **Criticality:** document it as a real field sourced from ServiceNow + declaration, **RoleBusiness-gated** (not "removed/roadmap"). **Test:** Preconditions section exists with the `snmp_exporter` bullet (`ifName`+`ifIndex-only`); README no longer contains "collapses to one node"; grounding guard re-reads `promclient/prom.go` harvest labels.
- [ ] **RG5 — Quickstart secrets + CI guards** (shared with I1). Quickstart uses `NEO4J_PASS`/`SERVICENOW_PASS`/`NAUTOBOT_TOKEN`, no `-*-pass`/`-*-token` flags; CI guard (shared w/ I1) fails on secret flags; second guard fails on the cross-product PromQL `instance=~"$instance", ifIndex=~"$ifIndex"`. **Test:** guard fails on a fixture containing the secret flags / cross-product line; passes on the corrected README.

**Effort:** Small-medium (mostly docs). **Risk:** if RG lands before RA, README documents unshipped PromQL — gate the tier-PromQL guards on RA's emitted fixture; land honest sections (Preconditions, when-NOT, competitive, phash, chokepoint) first.

## Workstream RI — device node / DEPENDS_ON exposure / topology overlays / multi-region (roadmap)

**New, roadmap altitude** — each item has a concrete first step. Layered on the hardened substrate; none make a P0/P1 task moot.

- [ ] **RI-2 (device node, medium first step).** `UpsertDeviceAndLink(ctx, devicePHash, deviceName, ifacePHash)` MERGE `(:Device{phash})` + `(i)-[:ON]->(d)`; call from `catalog.Sync`; `-backfill-devices` one-shot for existing graphs. Then `GET /devices/{device}/impact` (union InterfaceImpact over `ON` interfaces). **Unblocks:** device-/site-level impact ("FRA PoP router down"). **Test:** 3 ifaces / 2 devices → 2 Device nodes, 3 `ON` edges, idempotent; device-impact endpoint returns both apps with `interfacesHit`.
- [ ] **RI-3 (expose DEPENDS_ON, medium — highest value-per-effort; needs E1).** `ServiceDependents(ctx,svcPHash,at,maxDepth)` (reverse `DEPENDS_ON*1..N`, temporal `all()`, cap 10) + `ServiceDependencies` (forward); `GET /services/{svc}/dependents`, `/services/{svc}/dependencies`, `/apps/{app}/dependencies`. Behind RH authn. **Ship dependents-first** (the impact direction PromQL can't do). **Test:** `A→B→C`; `Dependents(C,5)={A,B}`, `depth=1={B}`; close `A→B` at t (append-only/E1) → `Dependents(C)` at>t `={B}`, at<t `={A,B}`.
- [ ] **RI-4 (topology/flow overlays, large; needs G2).** `nautobot.InterfaceConnections(ctx)` (paginated/G2) → `WriteTopologyAdjacency` append-only `(:Interface)-[:ADJACENT_TO {provenance:'topology',validFrom}]->(:Interface)` (mirror E1/TAKES). `cmd/promhash-reconcile` replays each declared Path's consecutive device transitions against `ADJACENT_TO`, emitting `promhash_declared_path_unsupported_total{app,reason}`. Roadmap later: Batfish/BGP-LS computed L3 paths + flow overlay (provenance precedence flow>topology>declared). **Test:** 2 links → 2 `ADJACENT_TO` edges (append-only, validTo null); a declared transition with no adjacency → counter +1, report names `(app,fromHop,toHop)`.
- [ ] **RI-5 (multi-region identity, medium; needs BL-2 decision).** Add `Region` to `Iface`; `-scrape-domain` flag through `promclient.New`/`catalog.Sync`; `ifacePHash = phash.Hash(KindIface, scrapeDomain, device, canonicalIfName)`; Resolver keyed by `(scrapeDomain,device)`. Device stays global (impact unions across regions). **Sequence after BL-2** (both rewrite `ifacePHash` — one migration). **Test:** phash differs across domains for the same `(device,ifName)`; same domain → equal; same `(instance,ifIndex)` under two domains → 2 nodes.

**RH target (roadmap):** replace StaticTokenStore with OIDC/JWT + per-customer scoping (RoleBusiness scoped to a customer allowlist enforced against `ImpactRow.Customer`); ship audit to Loki/ES. Chargeback/per-customer roadmap is **blocked on this**.

---

## Decisions still open

- **BL-2 — Interface identity model.** `(device, canonicalIfName)` vs `(device, ifIndex)` primary key. RA/RB use the stable `(instance, ifName)` join (works either way), but **F, RE-A2 (rename), RI-5 (region) all rewrite `ifacePHash`** — settle this before RE-A and RI-5 to avoid repeated identity migrations. Recommendation: ship F's cheap consistency fixes now; decide the primary-key question at the RE/RI gate.
- **BL-3 — History/index strategy.** OPT-9 (archive closed subgraphs) supersedes OPT-8 (relationship indexes) — pick one, both deferred; current fan-out is tiny.
- **Interim data-flow stopgap (RA).** Whether to ship the prior plan's 1-day "drop `job=`" stopgap to emit data while RA is built, or go straight to RA (recommended). Throwaway under the reshape.

## Self-review (against both reviews + the reshape)

- **Coverage:** review.md's 49 bugs map to Phase-0 workstreams (C/D/E/F/G/H/I) + RA (which subsumes/obviates the enrich P0s) + RC (criticality, OPT-12). review-observability.md's 12 Recs map: Rec1/9→RG, Rec2→RC, Rec3→RD, Rec4/5/6→RA, Rec7→RE+RC, Rec8→RF, Rec10→RB, Rec11→RH, Rec12→RI. All adversarial caveats land in RG4 (preconditions) or as design risks.
- **Conflict resolution:** criticality — RC/RD need it populated, RG/RH wanted it removed → **resolved: populate (RC) + RoleBusiness-gate (RH-1b)**, RG documents it as gated. ifOperStatus — review.md drop vs obs consume → **consume (RA2)**. Dashboard — prior B's two-list cross-product → **composite (RB)**.
- **Type/signature consistency:** `enrich.Selectors` returns `[]IfaceSelector` (RB1) reused by RA6/RB2; `JoinKey` threads RA1/RA2/RA4/RA5; `ImpactRow` designed once in RC3 and consumed by RC5/RC6/RD2/RH-1b; api `Repo` interface grows by `ListAllInterfaces`(C2)+`Ping`(H1)+`CatalogAge`(RF4)+`ListOpenDeclaredApps`(D3)+device/dep methods(RI) — **batch fakeRepo growth**.
- **Known soft spots:** RA2 needs a net-new `promqltest` harness (~½ day, flagged). Plugin TS behavioral tests gated on `tsc --noEmit` (no JS runner; adding jest is optional scope). RA is large → product-broken window longer than the prior in-place fix (interim stopgap flagged). BL-2 left open by design.

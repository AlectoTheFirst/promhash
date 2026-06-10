# promhash — Code & Use-case Review

**Date:** 2026-06-01
**Scope:** whole repo (Go monorepo + Grafana datasource plugin) and the design it implements.
**Method:** read the core code first-hand, then ran a multi-agent review (per-package finders → adversarial verification) plus four use-case tracks (cardinality/scaling, query performance, ops, model/API fit). 49 findings survived verification; 1 was rejected. Severities below are the post-verification ratings.

---

## Verdict

The architecture is genuinely good. The thesis — keep the many-to-many app↔interface relationship in a graph, shard cardinality *by application* with small per-app federation+recording-rule artifacts, and never put an `app` label on the firehose — is sound, and the code is clean, well-documented, and honestly scoped (the "known v1 simplifications" section is accurate).

**But the two headline outputs do not currently work end-to-end:**

1. The **curated per-app projection** (the federation + recording-rule artifacts) is generated in a form that **matches zero series** at evaluation time, and the federation selector **over-pulls** far past the declared path.
2. The **zero-cardinality dashboard pattern** (the marquee "free" path for non-curated apps) is **unreachable** — the Grafana template variables it depends on are documented but not wired, and the data shape returned can't drive a Prometheus `$instance/$ifIndex` variable.

Both are small, well-localized fixes. They are P0 because they sit on the critical path of the project's stated value, and unit tests don't catch them (the tests assert generated-string equality, never that the generated PromQL actually selects anything).

Below: P0 (use-case-breaking) → P1 (correctness/data-integrity) → P2 (robustness/ops) → concrete optimizations → full finding index.

---

## P0 — Breaks the documented use case

### 1. Recording rules match zero series (`job=` matcher vs `honor_labels: true`)
`internal/enrich/rules.go:38` stamps every rule expr with `job="promhash-fed-<app>"`, but `internal/enrich/tenant.go:14` sets `honor_labels: true` on the federation scrape. With `honor_labels: true`, Prometheus **keeps the federated series' original `job`** (e.g. `job="snmp"`) and does *not* overwrite it with the scrape `job_name`. So no federated series ever carries `job="promhash-fed-<app>"` → **every generated recording rule evaluates to empty**. The entire opt-in "first-class metrics" path silently produces no data.

Masked because `rules_test.go` only asserts string equality of the YAML, never that the matcher intersects the federated label set.

**Fix:** drop the `job=` matcher from the rule expr (the federation already scopes the tenant to this app's slice) — match on `instance`+`ifIndex` only. Update `payments.golden.yaml` and `rules_test.go`, and add a test that asserts the rule matcher and the federated label set actually intersect.

### 2. `FederationMatch` builds a cross-product, not the path
`internal/enrich/federation.go:32-33` collects all hop instances into one regex and all ifIndexes into another:
```
{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1|10.0.0.2", ifIndex=~"42|43"}
```
Prometheus ANDs these as two independent matchers, so the selector matches the **cross product** `{instances} × {ifIndexes}`. For the 2-hop test path it pulls 4 series — including phantom `(10.0.0.1, 43)` and `(10.0.0.2, 42)` that belong to *other* interfaces/apps. **`ifIndex` is only unique per device, never globally**, so on a real backbone where ifIndex 42 and instance 10.0.0.1 recur across many devices, the federated slice balloons as `I × X` instead of the `N` real hops. That directly regresses the "pull only that app's slice" thesis and inflates the cardinality the tenant ingests.

(The recording rules use exact `instance=`/`ifIndex=` pairs, so the *final* app series stay correct — but only the ones the rule still matches after P0-1 is fixed. The over-pull is wasted federation + tenant cardinality.)

**Fix:** emit one exact-match selector per hop and pass `match[]` as a list (Prometheus `/federate` accepts repeated `match[]`). Thread a `[]string` through `FederationMatch` → `cmd/promhash-enrich` → `TenantScrapeConfig`.

### 3. Zero-cardinality dashboard pattern is unreachable
This is the README's marquee "free" pattern for non-curated apps. Two breaks:
- **No Grafana variable mechanism exists.** `plugin/.../src/datasource.ts:6` extends `DataSourceWithBackend` and adds an unused `apps()` helper, but implements neither `metricFindQuery()` nor `VariableSupport`/`CustomVariableSupport`. A user literally cannot create the documented `apps` or `path_interfaces/$app` template variables. (`VariableSupport` is available in the installed `@grafana/data` but unused.)
- **Wrong data shape.** `plugin/.../pkg/plugin/resource.go:22` maps `path_interfaces/<app>` → `/apps/<app>/path` and forwards the body verbatim — a list of full hop objects (`{device, ifName, metricIfName, instance, ifIndex, ...}`). A PromQL panel variable needs flat `instance`/`ifIndex` value lists, not hop objects.

**Fix (end-to-end):** add `GET /apps/{app}/selectors` returning `{"instances":[…],"ifIndexes":[…]}` (dedup+sorted, reusing the dedup logic factored out of `FederationMatch`); add plugin resource routes `path_instances/<app>` / `path_ifindexes/<app>` returning flat arrays; implement `CustomVariableSupport`/`metricFindQuery` in `datasource.ts` + a variable query editor; point README §"zero-cardinality" at the two variables.

### 4. Impact API is unusable with natural interface names
`internal/api/server.go:113,132` computes `phash.Hash(KindIface, device, ifName)` from the **raw** query params, but the catalog stores interfaces under the **canonical** ifName (`internal/catalog/sync.go:32`, `ifacePHash(device, canon)`). So a caller using a natural name (`Te0/1/2`, or the README's own example values) gets a phash that never matches a stored node → `/impact` and `/interface-apps` silently return "no path known"/empty. The API offers no resolver on the read path, so the caller must already know the exact canonical form (`tengige0/1/2`).

**Fix:** run the same canonicalization (and ideally the `Resolver`) server-side before hashing, or expose an interface-lookup endpoint. See also "collapse /impact and /interface-apps" below.

---

## P1 — Correctness & data integrity

- **Deleting a declaration YAML never retracts the app** (`cmd/promhash-loader/main.go:45`, **high**). The loader only processes files *present* on disk; nothing closes validity for apps whose YAML was removed. Decommissioned apps stay `valid_to IS NULL` forever — contradicting the git-as-source-of-truth model and producing phantom apps/selectors. *Fix:* reconcile — list open declared apps, diff against the present file set, `CloseAppValidity` for the difference (a tombstone pass).

- **Declaration load is non-atomic** (`internal/declare/load.go:39-42`, **medium**, ops-impact high). `CloseAppValidity` (3 writes) + `UpsertDeclaredApp` (1 write) run as 4 separate auto-committed transactions. A crash mid-load leaves the app with all edges closed and none recreated → it vanishes from current state. *Fix:* wrap close+upsert in one `session.ExecuteWrite` (also gains managed-transaction retry the current `ExecuteQuery` path lacks).

- **`DEPENDS_ON` reopen destroys temporal history** (`internal/graph/repo.go:168`, **medium**, latent). `MERGE (svc)-[do:DEPENDS_ON]->(target)` has no temporal discriminator, so on reload it re-matches the edge `CloseAppValidity` just closed and reopens it — overwriting `validFrom`, leaving exactly one ever-restarting edge. Asymmetric with `TAKES`/`Connection`, which are append-only and correct. No served query reads `DEPENDS_ON` today, so nothing is wrong *now*, but the temporal invariant is violated. *Fix:* include `validFrom` in the MERGE pattern (or CREATE fresh + rely on close).

- **Single wall-clock `now` for both close and open** (`cmd/promhash-loader/main.go:46`, **medium**). The same `now` is the prior revision's `validTo` and the new revision's `validFrom`. Two loads in the same second → a `[T, T)` zero-width window that no point-in-time query can ever hit → that revision is silently un-queryable. *Fix:* make `validFrom` strictly increasing (git commit time, or detect/ bump a same-second collision).

- **Device-name identity is inconsistent** (`internal/phash/phash.go:34` vs `internal/catalog/resolver.go:47`, **medium**). `phash.Hash` lowercases/trims, so `Rtr1` and `rtr1` collapse to one Interface node (silent merge). But `Resolver.byDevice` keys on the **exact** device string, so the same case variance fails to resolve. Device names need one normalization rule applied at both sites (and validated by `keySafe`, which currently only guards `app`/`runs_as`/customer/`dep.to`).

- **Empty interface ref collapses interfaces** (`internal/catalog/sync.go:27-30` + `resolver.go`, **medium**). If both `ifName` and `ifDescr` canonicalize to `""`, the phash is `Hash(KindIface, device, "")` — every such interface on a device MERGEs into one node. And `Resolver.Resolve("", …)` happily binds to an interface with empty `ifDescr`/`ifAlias`. *Fix:* reject empty canonical names at sync and empty refs at resolve.

- **Candidate paths flattened → double-count** (`internal/enrich/rules.go:21`, **medium**). `RuleGroup` iterates hops with no dedup by `(instance, ifIndex, direction)`. `AppPath`'s `DISTINCT` includes `seq`, so the same interface appearing in two candidate paths at different `seq` yields two identical recording rules (same record name + labels + expr) → duplicate series / eval conflict, and the README's "no cross-path summation" intent is only half-honored. *Fix:* dedup rules by `(instance, ifIndex, direction)`; keep candidate identity separately (see optimization below).

- **Non-strict YAML parsing** (`internal/declare`, **medium**). Unknown/typo'd fields are silently ignored (`paths`/`path`, `runs_as`, etc.), so a misspelled key just drops data with no CI error. *Fix:* `yaml.Decoder.KnownFields(true)` (or equivalent strict decode).

- **`criticality` is a phantom field** (`internal/graph/model.go:54`, `repo.go:301`, **low** but documented). `ImpactRow.Criticality` is returned by `/impact` and shown in the README (`"criticality":"tier-1"`), but nothing ever sets `Application.criticality` — not `UpsertDeclaredApp`, not `UpsertAppSeed`, not the YAML schema. Always empty. *Fix:* either populate it (declaration field and/or ServiceNow seed) or remove it from the model and docs.

- **Interface rename orphans the old node** (`internal/catalog/sync.go`, **medium**). A renamed interface gets a new phash → new node; the old node and its `HOP` edges are never reconciled, so old declared paths keep pointing at a stale interface. *Fix:* reconcile by `(device, ifIndex)` continuity or mark superseded.

---

## P2 — Robustness & operations

- **No request timeout on the Prometheus client** (`internal/promclient/prom.go` + `cmd/promhash-catalog/main.go:32` uses `context.Background()`), so `HarvestInterfaces` can hang a scheduled job forever. Same for Nautobot/ServiceNow. *Fix:* `context.WithTimeout` around each upstream call.
- **CMDB pagination truncation** (medium): `servicenow.Applications()` fetches a single page (no `sysparm_limit`/`offset`); `nautobot.DeviceInstanceMap` relies on `?limit=0` with no next-link fallback. Large tables silently truncate → missing devices/apps. *Fix:* follow pagination links.
- **Zero self-observability** on `promhash-api`: no `/healthz`, `/readyz`, or `/metrics`, no request logging, no panic recovery, no per-request DB deadline. *Fix:* add the three endpoints (`/readyz` pings Neo4j), a metrics middleware (`promhttp`), recover middleware, and a context timeout around repo calls.
- **Catalog staleness is invisible**: `Sync` never retires vanished interfaces and nothing alerts on `observedAt` age, so the resolver can bind to a dead interface. *Fix:* expose `promhash_catalog_age_seconds`, log oldest `observedAt`, and decide a retire policy (mark `stale=true` rather than serve as live).
- **`-validate-only` requires a live, populated Neo4j** (`cmd/promhash-loader`): a Neo4j outage fails PRs that contain perfectly valid YAML, coupling the CI gate to infra availability. *Fix:* allow a catalog snapshot (file/cache) for validation.
- **Secrets in the README quickstart** are passed as CLI flags (`-neo4j-pass`, `-servicenow-pass`, `-nautobot-token`), leaking into the process list — contradicting the tools' own secrets-via-env design. *Fix:* update README to use env vars throughout.
- **`log.Fatal` on every error path** skips the deferred Neo4j `Close` (and the API's graceful shutdown) — minor, but worth a clean exit.
- **CI** pins Go to an exact patch but pins actions to mutable major tags; `promhash-seed` ignores the `EnsureConstraints` error (can seed without uniqueness constraints); `Makefile` has no combined `ci` target and omits the plugin module.

---

## Concrete optimizations (use-case & performance)

Prioritized; each is grounded in a specific file.

**Scaling / enrichment**
1. **Per-hop exact selectors** (P0-2 fix) — bounds the federated slice to the real path. *high / small.*
2. **Drop the broken `job=` matcher** (P0-1 fix) — makes curated series actually appear. *high / small.*
3. **Mapping-metric model instead of regenerate-on-renumber.** Emit one bounded `promhash_interface_app{instance,ifIndex,app,service,device,ifName} 1` series per declared (interface,app), then compute rates with `rate(ifHCOutOctets[5m]) * on(instance,ifIndex) group_left(app,service) promhash_interface_app{direction="egress"}`. Collapses N per-hop rules into 2 group_left rules/direction and removes the silent-wrongness window when `ifIndex` renumbers mid-series. *high / large.*
4. **Emit the tenant scrape-config from `promhash-enrich`** (today `TenantScrapeConfig` is effectively dead code) and stop federating `ifOperStatus` (no rule consumes it). *medium / small.*
5. **Correct the README "bounded duplication" claim** and document shared-core-link cost; consider a shared core-link tenant. *medium / medium.*

**Neo4j query performance** (constraints today cover only `phash`)
6. `CREATE CONSTRAINT app_name_unique IF NOT EXISTS FOR (a:Application) REQUIRE a.name IS UNIQUE` — backs `ListApps`' `ORDER BY name` and `AppServiceName`'s name lookup (currently a label scan). *high / small.*
7. Composite index `FOR (n:Interface) ON (n.device, n.ifName)` for catalog rebuild and reverse lookups. *medium / small.*
8. Relationship range indexes `FOR ()-[t:TAKES]-() ON (t.validFrom)` / `(t.validTo)`, and a `validTo IS NULL` fast path for the default (now) queries, so point-in-time queries seek instead of scanning all closed history. *high / medium.*
9. **Archive closed `Connection`/`Path` subgraphs** past a retention horizon onto an archival `:USED` edge so `AppPath`/`InterfaceImpact` fan-out stays bounded by current+recent declarations rather than all-time history. *high / large.*
10. **Cache `ListAllInterfaces` and `ListApps`** in the long-lived API/enrich process (they re-query every call). *medium / medium.*

**Model / API ergonomics**
11. **Wire the zero-cardinality variables end to end** (P0-3 fix). *high / medium.*
12. **Make "potential vs active" impact honest** — add `provenance`/`candidateCount` (or a `potential` flag) to `ImpactRow` from the `TAKES` provenance/confidence + sibling-candidate count, and document that impact is *potential* until flow overlay (Layer 2). *high / medium.*
13. **Preserve candidate-path identity in `AppPath`** (return a `pathId`/candidate, order by `(candidate, seq)`) so ECMP/alternates aren't flattened in health panels and rules. *high / medium.*
14. **Collapse `/impact` and `/interface-apps`** into one honest surface (they run the identical `InterfaceImpact` query; only the JSON wrapper differs). *medium / small.*
15. **Pagination/filtering on `/apps`** before the set grows. *low / small.*

---

## What's good (keep)

- The graph-vs-projection split, the cardinality law, and honest-provenance/temporal model are the right design and are mostly faithfully implemented.
- `CloseAppValidity` is carefully written (the comment explaining why each close re-`MATCH`es independently is correct and non-obvious).
- `phash` separator choice (`\x1f`) + `keySafe` rejecting `:` and control chars is a thoughtful injection guard (just apply it to device names too).
- Test discipline is good (unit vs `//go:build integration`, golden files) — the gap is *what* the golden tests assert (string equality, not PromQL behavior), not that they exist.
- The API correctly returns explicit `[]`/`"no path known"` instead of null for the path/impact endpoints (note: `/interface-apps` is the exception — it serializes nil as `null`).

---

## Appendix — all 49 verified findings by severity

**High (2)**
- declare-loader: deleting a YAML never retracts the app (stale "current" forever).
- grafana-plugin: `apps()`/`path_interfaces` variable queries documented but not wired — template variables can't work.

**Medium (18)**
- graph-cypher: `DEPENDS_ON` reload reopens/overwrites the edge, destroying dependency history.
- graph-cypher: single `now` for close+open → zero-width un-queryable windows on same-second reloads.
- phash: API read path hashes raw (un-canonicalized) ifName → never matches catalog phash.
- phash: normalization folds case/edge-whitespace but `keySafe` permits internal whitespace + Unicode case variants → distinct entities can merge.
- phash: device name hashed with only lower/trim, no key-safety → case-variant hostnames merge or fail to match.
- catalog: empty interface ref silently resolves to an interface with empty ifDescr/ifAlias.
- catalog: interface rename orphans the old node; old node + HOP edges never reconciled.
- declare: load is non-atomic (4 separate auto-committed writes) → crash corrupts temporal state.
- declare: non-strict YAML — unknown/typo'd fields silently ignored.
- enrich: multiple candidate paths flattened → double-counts shared interfaces.
- enrich: recording-rule output ordering non-deterministic across multi-path apps → noisy git diffs.
- enrich: rule label values stamped into YAML flow map unescaped.
- api: does not canonicalize ifName → natural names yield empty impact.
- http: Prometheus client has no overall request timeout — `HarvestInterfaces` can hang forever.
- http: ServiceNow `Applications()` fetches one page (no limit/offset) → truncates large CMDB.
- http: Nautobot `DeviceInstanceMap` relies on `?limit=0`; no next-link fallback if server caps results.
- grafana-plugin: zero-cardinality pattern undriveable — `path_interfaces` returns hop objects, not value lists.
- grafana-plugin: QueryEditor omits the `device`/`ifName` inputs the impact query needs.
- cmd/ci: README quickstart leaks secrets via CLI flags (contradicts secrets-via-env).

**Low (29)** — see the live table; notable ones:
- graph: `AppServiceName` scans all `Application` (no `name` index); Connection/Path accumulate unbounded across reloads; `criticality` read but never written.
- catalog: Arista `Et` (and other) abbreviations not canonicalized; `abbrev` "longest-prefix-first" comment is wrong (matching is exact-equality, self-mapping entries dead); `NoMatchError` "did you mean:" empty for unknown device; `Sync` has no dedup/conflict detection; Interface nodes carry no validity interval (violates the stated invariant).
- declare: `valid_from` is load wall-clock, not commit time; a dependency with no candidate paths passes validation and creates a path-less Connection; loader ignores `filepath.Glob` error (empty dir = passing CI gate).
- enrich: `match[]` can produce invalid single-quoted YAML for instances containing a quote; `ifOperStatus` federated but never used.
- api: `/interface-apps` serializes nil as `null` and lacks the "no path known" marker; empty `device`/`ifName` accepted and hashed; `at` accepts negative/far-future timestamps unbounded.
- http: no retry/backoff in any client; ServiceNow/Nautobot discard error-response bodies; `HarvestInterfaces` swallows non-vector results and `ifIndex` parse errors.
- grafana-plugin: no default `queryType` (undefined silently runs empty impact query); `CheckHealth`/`getJSON` discard upstream error detail; `apiUrl` trailing slash / empty not validated.
- cmd/ci: `log.Fatal` skips deferred driver `Close`; `promhash-seed` ignores `EnsureConstraints` error; CI pins actions to mutable major tags; `Makefile` lacks a combined `ci` target and omits the plugin module.

**Rejected (1):** one finder claim did not survive verification against the actual code.

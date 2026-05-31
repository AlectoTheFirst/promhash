# promhash — Network-Metric → Business-Application Mapping (v1 Design)

- **Date:** 2026-05-30
- **Status:** Draft for review
- **Author:** brainstormed with Claude Code

## 1. Problem

Classic infrastructure exporters (snmp_exporter, blackbox_exporter, node_exporter)
produce metrics keyed by infrastructure identity — `instance`, `ifIndex`, device.
The business does not think in interfaces; it thinks in applications, services, and
customers. There is a gap between three worlds that never share a reliable key:

- **ServiceNow CMDB** — `app → CI → IP`, ownership, criticality. A correlation/CI id
  exists on *some* assets, not all.
- **Network telemetry in ELK** — router/firewall syslog and firewall accept/drop logs
  (5-tuple, no interface index). NetFlow exists only via Plixer Scrutinizer and only
  for **core routers**, not office/edge sites.
- **Prometheus** — millions of infra time series, app-free.

Three facts make the naive solution impossible:

1. **Many-to-many.** One interface (`ifHCInOctets{instance,ifIndex}`) carries many apps
   and customers at once. A Prometheus label holds **one** value, so there is no single
   `app=` to stamp. Scrape-time relabel, single `group_left`, and blanket per-interface
   tagging all fail.
2. **Cardinality.** 10k devices × N interfaces is already large. Multiplying that by an
   app dimension is a cardinality bomb. Adding app labels to the firehose is off the table.
3. **Partial coverage.** Flow data (the only ground truth for "which app traverses what")
   reaches the core, not the edge. Any honest design must degrade gracefully and never
   present inferred mapping as observed.

Recording rules *can* express the mapping, but evaluated against the firehose they do not
scale — the original concern that motivated this work.

## 2. Core Insight

Shard the cardinality by application instead of multiplying it. Keep the relationship in a
**graph outside Prometheus**, and for the applications that need it, generate **per-app
recording rules that run over only that app's path** (tens–hundreds of series, not millions).
Same recording-rule mechanism, ~1000× smaller working set — the scalability concern
dissolves. Many-to-many stops mattering: a shared core link simply appears in each app shard
that uses it (duplicated across a small curated set), never as a multi-valued label.

## 3. Goals / Non-Goals (v1)

**Goals**
- A property-graph model of application paths — directional, reified connections,
  east-west dependency, CSDM-aligned business layer, stable interface identity, and
  first-class **provenance + temporal validity** on every fact.
- Manual/declared path data, authored as **YAML-in-git**, synced into the graph.
- An **enrichment process** that, for a **curated** set of apps, generates federation
  selectors + recording-rule groups as GitOps artifacts.
- Per-curated-app federation/tenant that re-exposes app-labeled series.
- A **consumption API** answering app-path-health and impact for *all* apps (including the
  flow-blind edge), consumed by Grafana.

**Non-Goals (explicitly out of v1)**
- Automated flow-based path discovery (Scrutinizer ingestion).
- Topology/routing-computed paths from Nautobot L1/L2/L3.
- Quantitative byte attribution / per-customer chargeback (Layer 2). The model is built to
  accept it later without remodel.
- Write-back of corrections to ServiceNow CMDB.
- Physical dedicated Prometheus process per app (use logical tenants / curated only).
- Any `app` label on the high-cardinality infra metrics. (Permanent law, not a v1 limit.)

## 4. Principles

- **Cardinality law.** Infra metrics never get an `app` dimension. App context lives in the
  graph; per-app series are a separate, small, curated metric family.
- **Honest provenance.** Every edge records how it was derived (`declared | flow | topology`)
  and a confidence. Dashboards must distinguish observed from inferred.
- **Temporal by default.** Mutable facts carry `valid_from/valid_to`. Current state =
  `valid_to IS NULL`; point-in-time impact filters the interval.
- **Source of truth for declared data is git.** The graph is a queryable materialization,
  not the system of record for manually-entered facts — PR review, history, reproducibility
  for free, matching the existing GitOps culture.
- **Curated-only enrichment.** Per-app series exist only for apps that need PromQL
  `sum by(app)` / SLOs. Everything else is served by the graph API at zero cardinality cost.

## 5. Architecture

```
  Nautobot (topology/IPAM) ─┐  one-shot seed import (typed nodes, not paths)
  ServiceNow (app→CI→IP)    ├─────────────────────────┐
                            │                          ▼
  declared paths (YAML in git) ──loader──►  ┌───────────────────────┐
                                            │  promhash graph (Neo4j)│
                                            │  CSDM business layer,  │
                                            │  reified Connection/   │
                                            │  Path, temporal facts, │
                                            │  hashed identity nodes │
                                            └───────────┬───────────┘
                                                        │
                         ┌──────────────────────────────┼───────────────────────────┐
                         ▼ (curated apps)                ▼ (all apps)                 │
              enrichment generator               consumption API                     │
              graph → GitOps artifacts:           GET /apps/{a}/path                 │
                • federation match[]              GET /interfaces/{d}/{i}/apps        │
                • recording-rule group            GET /impact?...                     │
                         │                                ▼                           │
                         ▼                         Grafana (Infinity/JSON):           │
   main infra Prometheus ──federate slice──►       path-health panels, template      │
     (app-free, bounded)        per-app tenant      vars, alert enrichment            │
                                (app-labeled, tiny) ──remote_write──► cloud LTS ◄─────┘
```

## 6. Components

Each component has one purpose, a defined interface, and explicit dependencies.

### C1 — Graph store (Neo4j) + data model
- **Purpose:** hold the application-path graph; answer path-health and impact traversals.
- **Interface:** Cypher / Bolt. Read by the enrichment generator and consumption API.
- **Identity:** every node carries `phash` (see C2) as its `MERGE` key.

**Layer A — Business (CSDM-aligned).** Customer dimension + clean ServiceNow mapping:
```
(:Customer)-[:CONSUMES]->(:BusinessService)
(:BusinessService)-[:REALIZED_BY]->(:ApplicationService)
(:Application)-[:RUNS_AS]->(:ApplicationService)        // CSDM Business App → App Service
```

**Layer B — Logical connectivity (reified, directional).**
```
(:ApplicationService)-[:EXPOSES|:USES]->(:Endpoint)     // Endpoint = ip:port:proto on a Segment
(:Connection)-[:FROM]->(:Endpoint)                      // client/source (direction intrinsic)
(:Connection)-[:TO]->(:Endpoint)                        // server/dest
(:ApplicationService)-[:DEPENDS_ON]->(:ApplicationService)  // east-west; realized by Connection(s)
```
`Connection` is reified — carries `port, proto` plus the cross-cutting fact props. `DEPENDS_ON`
is declared or rolled up from realizing Connections.

**Layer C — Physical path (ordered, the anchor).**
```
(:Connection)-[:TAKES]->(:Path)                         // Path reified → shareable / redundant
(:Path)-[:HOP {seq, direction}]->(:Interface)           // ORDERED hops; direction ingress|egress|transit
(:Interface)-[:ON]->(:Device)
(:Interface)-[:MEMBER_OF]->(:Segment)                   // SVI/trunk where relevant
(:IP)-[:ASSIGNED_TO]->(:Interface)
```
`Interface` is keyed by **`(device_phash, ifName)`** — stable across reboot/reconfig.
**`ifIndex` is a time-varying *attribute*, never the key**; the enrichment generator resolves
the current `ifIndex` at generation time.

**Cross-cutting fact properties** (on every mutable edge/node — `DEPENDS_ON`, `Connection`,
`Path`, `HOP`, `EXPOSES/USES`, `MEMBER_OF`, `ASSIGNED_TO`):
`{provenance: declared|flow|topology, confidence, source, valid_from, valid_to, observed_at}`.

- **Constraints/indexes:** unique constraint on `phash` per label; index on `Interface.ifName`,
  `Device.name` for impact lookups.
- **Dependencies:** none (new component). Memgraph is a drop-in (Cypher-compatible).

### C2 — Identity resolution ("promhash" core)
- **Purpose:** collapse the same real-world entity, seen as IP / FQDN / sys_id / serial /
  Nautobot-id, into one node so manual entry and future automated feeds dedupe instead of
  forking the graph.
- **Interface:** `phash(entity_type, canonical_keys) -> stable id`. Pure, deterministic.
  Canonicalization per type:
  - `Device`: serial → hostname → nautobot-id (first available, normalized).
  - `Interface`: `(device_phash, normalize(ifName))`.
  - `IP`: normalized address + VRF/tenant scope.
  - `Endpoint`: `(ip_phash, port, proto)`.
  - `ApplicationService` / `Application`: CI sys_id → canonical name.
- **Dependencies:** none. Used by the loader (C4) and seed importers (C3) on every upsert
  (`MERGE` on `phash`). Collisions must fail loud, never silently merge two real entities.

### C3 — Seed importers (one-shot, optional)
- **Purpose:** populate *typed* nodes (devices, interfaces, IPs from Nautobot; apps, app
  services, CIs, app→IP from ServiceNow) so declared YAML references resolve against real
  entities. Seeds nodes, **not** paths.
- **Interface:** CLI/job reading Nautobot + ServiceNow APIs, upserting via C2 hashes.
- **Dependencies:** Nautobot API, ServiceNow API, C1, C2.

### C4 — Declared-path loader (YAML-in-git → graph)
- **Purpose:** the manual-entry surface for v1. Humans author declarations in YAML in a git
  repo; PR review = data review; merge triggers a sync into Neo4j. The full schema is hidden
  behind a lean declaration — the loader synthesizes Endpoint/Connection/Path/Hop nodes with
  `provenance=declared`, `source=<git sha>`, `valid_from=<commit time>`.
- **Interface — declaration format:**
  ```yaml
  app: payments
  runs_as: payments-api
  consumed_by_customers: [acme, globex]      # → Customer / BusinessService / REALIZED_BY
  depends_on: [ledger-api, auth-api]          # → DEPENDS_ON (east-west)
  paths:                                       # → Path + ordered HOPs
    - to: ledger-api
      hops:
        - {device: rtr-core-1, ifName: Te0/1/2, direction: egress}
        - {device: rtr-core-2, ifName: Te0/2/1, direction: transit}
  owner: team-payments
  ```
  Removal from YAML closes the edge's validity interval (`valid_to=<commit time>`), never a
  hard delete — preserves point-in-time history.
- **Dependencies:** C1, C2, git.

### C5 — Enrichment generator (graph → GitOps artifacts)
- **Purpose:** for each **curated** app, materialize its current interface set and emit deploy
  artifacts. The mechanical half, once the graph knows the path.
- **Interface:** runs the app-path-health traversal (§7), resolves each `Interface` to its
  *current* `ifIndex`, then writes two artifacts per app into a git repo:
  - **federation `match[]`** selecting that app's series, e.g.
    `{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"rtr-core-1|rtr-core-2", ifIndex=~"42|43"}`
  - **recording-rule group**, direction-aware (HOP `direction` → `ifHCInOctets` vs
    `ifHCOutOctets`), e.g.
    ```yaml
    groups:
    - name: promhash_payments
      rules:
      - record: app:if_egress_octets:rate5m
        expr: rate(ifHCOutOctets{job="promhash-fed-payments"}[5m])
        labels: { app: payments, service: payments-api, coverage: declared }
    ```
- **Dependencies:** C1, git, curated-app allowlist (config), `ifName→ifIndex` resolution
  (Nautobot or an SNMP ifName/ifIndex map).

### C6 — Per-app federation/tenant (GitOps deploy)
- **Purpose:** run the generated artifacts. Each curated app = a logical tenant / rule
  instance that federates only its slice from the main infra Prometheus, evaluates the
  per-app recording rules, and remote_writes app-labeled series to the cloud LTS.
- **Interface:** standard Prometheus federation (`/federate?match[]=…`) or `remote_read`;
  config delivered by existing GitOps tooling.
- **Dependencies:** C5 artifacts, main infra Prometheus, cloud LTS.

### C7 — Consumption API + Grafana integration
- **Purpose:** serve the graph to humans and alerting for *all* apps, including flow-blind
  edge, at zero cardinality cost.
- **Interface (REST, Cypher-backed):**
  - `GET /apps/{app}/path` → ordered `[{device, ifName, ifIndex, direction, provenance, confidence}]`
  - `GET /interfaces/{device}/{ifName}/apps` → impacted apps / services / customers
  - `GET /impact?device=&ifName=&at=<ts>` → blast radius at a point in time (apps, owners,
    customers, criticality)
  - Grafana Infinity/JSON datasource consumes these for template variables, path-health
    panels, and alert-notification enrichment.
- **Dependencies:** C1.

## 7. Data Flow

**App-path-health traversal (anchor query):**
```cypher
MATCH (a:Application {phash:$app})-[:RUNS_AS]->(svc:ApplicationService)
MATCH (svc)-[:EXPOSES|:USES]->(:Endpoint)<-[:FROM|:TO]-(c:Connection)
WHERE c.valid_to IS NULL
MATCH (c)-[:TAKES]->(:Path)-[h:HOP]->(i:Interface)-[:ON]->(d:Device)
RETURN DISTINCT d.name, i.ifName, h.seq, h.direction ORDER BY h.seq
```
`DISTINCT` interface set → federation `match[]`; `h.direction` → in/out metric selection.
Reverse the same edges → impact/blast-radius.

**Curated app (e.g. payments):**
1. Owner declares path in YAML → PR → merge → C4 syncs Connection/Path/Hop edges with
   provenance + validity.
2. C5 runs the traversal, resolves current `ifIndex`, writes federation `match[]` +
   direction-aware recording rules to the GitOps repo.
3. GitOps deploys to the payments tenant (C6); it federates its slice, evaluates rules,
   remote_writes `app:…{app="payments",coverage="declared"}` to LTS.
4. Grafana dashboards/alerts query app-labeled series directly with `sum by(app)`.

**Graph-only app (flow-blind edge, not curated):**
1. Path declared/seeded the same way.
2. No per-app series generated. Grafana path-health panel resolves `app → ordered interfaces`
   via C7, then queries the **raw** infra metrics filtered to that set; impact alerting calls
   `/impact` on interface-down. No new cardinality.

## 8. Cardinality Analysis

- Main infra Prometheus: unchanged (app-free).
- Per curated app: ~path-size series (tens–hundreds). For K curated apps, added series
  ≈ Σ path_size(app) — bounded, horizontally shardable, not multiplicative over the firehose.
- A core link traversed by several curated apps is recorded once per app shard. Acceptable
  because the curated set is small and intentional; revisit only if K grows large.

## 9. Provenance, Confidence & Temporal Model

- Fact props: `provenance ∈ {declared, flow, topology}`, `confidence`, `source`
  (git sha / Scrutinizer / Nautobot), `valid_from`, `valid_to`, `observed_at`.
- Current state = `valid_to IS NULL`. Point-in-time queries filter
  `valid_from <= $at < coalesce(valid_to, +∞)`.
- Derived series carry `coverage` label = edge provenance, so dashboards render "declared"
  differently from future "flow-observed".
- v1 writes `provenance=declared` only; schema is forward-compatible with automated feeds.

## 10. Failure Modes & Handling

- **Stale graph vs reality:** declared edges age; `observed_at` surfaces staleness. Report,
  don't silently trust. Auto-correction is out of scope for v1.
- **Missing path:** app with no declared interfaces → C5 emits nothing and logs the gap; C7
  returns an explicit "no path known" marker, never an empty success that reads as "no impact".
- **Conflicting edges:** declared wins in v1 (only source). When flow/topology arrive,
  precedence = highest confidence, ties broken `flow > topology > declared`; conflicts logged.
- **Unstable ifIndex:** graph keys on `ifName`; ifIndex resolved at generation time, so a
  renumber regenerates rules rather than silently mislabeling. Resolution miss = loud failure.
- **Federation source load:** `match[]` is graph-scoped to path interfaces only, bounding
  `/federate` cost; monitor source-instance load.
- **Identity collisions:** `phash` canonicalization is the risk surface; collisions fail loud
  (unit-tested per entity type), never silently merge two real entities.

## 11. Testing Strategy

- C2 `phash`: table-driven canonicalization + collision tests per entity type.
- C4 loader: YAML → graph upsert/retract (validity-close) round-trips against ephemeral Neo4j.
- C5 generator: golden-file tests — fixture graph → expected federation `match[]` +
  direction-aware rule-group YAML; ifIndex-resolution cases.
- C7 API: contract tests over a seeded graph (path order, impact, point-in-time `at`).
- End-to-end smoke: declare a fixture app → generate → load rules into a throwaway Prometheus
  → assert app-labeled series appear.

## 12. Future (post-v1)

- **Layer 2 attribution:** ingest Scrutinizer NetFlow (core) + firewall bytes (boundaries),
  attach byte-shares to existing `Connection`/`Path` nodes, split interface bytes across apps;
  emit curated `app:net_bytes:rate` with honest `coverage`. The reified model already supports
  this with no remodel.
- **Automated path discovery:** flow-observed (core) and topology/routing-computed (Nautobot)
  HOP edges added with their own provenance.
- **Chargeback / per-customer** quantitative views once attribution exists.

## 13. Open Questions

- Neo4j vs Memgraph final pick (both Cypher; defer to ops preference — non-blocking).
- Curated-app allowlist governance: who decides an app graduates to per-app series?
- Federation vs remote_read for the slice (default: federation).
- Where per-app tenants physically run vs the cloud LTS tenancy model.
- `ifName → ifIndex` resolution source of truth (Nautobot vs live SNMP walk).

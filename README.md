# promhash

**Map classic network and infrastructure metrics to the business applications that depend on them — without ever exploding Prometheus cardinality.**

promhash bridges three worlds that rarely share a reliable key: the asset/application records in a CMDB (ServiceNow), network telemetry, and the millions of infrastructure time series in Prometheus (`snmp_exporter`, `blackbox_exporter`, `node_exporter`, and friends). It holds the application-to-infrastructure relationship in a property graph, and uses that graph to answer two questions cheaply:

- **App path health** — *given an application, which devices and interfaces does its traffic cross, and are any of them saturated or down?*
- **Impact / blast radius** — *this interface or device just failed; which applications and customers are affected?*

The infrastructure metrics themselves are never tagged with an `app` label. The relationship lives in the graph, and is projected into Prometheus only where it is needed and affordable.

---

## Contents

- [Why this exists](#why-this-exists)
- [How it works](#how-it-works)
- [Data model](#data-model)
- [Requirements](#requirements)
- [Quickstart](#quickstart)
- [Declaring application paths](#declaring-application-paths)
- [Command-line tools](#command-line-tools)
- [HTTP API](#http-api)
- [Grafana datasource plugin](#grafana-datasource-plugin)
- [Enrichment and federation](#enrichment-and-federation)
- [How-to recipes](#how-to-recipes)
- [Codebase reference](#codebase-reference)
- [Development](#development)
- [Design principles](#design-principles)
- [Roadmap](#roadmap)

---

## Why this exists

A network interface is shared. A single `ifHCInOctets{instance="rtr-core-1",ifIndex="42"}` series carries traffic for many applications and customers at once. That makes the obvious approaches break:

- **You cannot stamp a single `app` label** on a shared interface — there is no single application. The relationship is many-to-many.
- **You cannot fan out** one series per `(interface, app)` pair — across thousands of devices that is a cardinality explosion.
- **You cannot rely on flow data everywhere** — high-fidelity flow records typically cover the core, not every edge or office site.

promhash keeps the many-to-many relationship in a graph, where it belongs, and shards any per-application time series by application so the working set stays small. Recording rules that were unaffordable against the full firehose become trivial when scoped to a single application's path.

---

## How it works

```
  Nautobot (topology / IPAM) ─┐  device <-> instance map, optional seed
  ServiceNow (app -> CI -> IP) ┼───────────────────────────┐  seed (typed nodes)
  Prometheus interface labels ─┘  catalog sync (real ifName/ifDescr/ifAlias/ifIndex)
                              │                             ▼
  declared paths (YAML in git) ── loader (CI-validated) ──► ┌─────────────────────┐
                                                            │  Neo4j graph        │
                                                            │  business + paths,  │
                                                            │  provenance + time  │
                                                            └─────────┬───────────┘
                          ┌──────────────────────────────────────────┼──────────────────────┐
                          ▼ curated apps                              ▼ every app            │
                enrichment generator                          HTTP API service               │
                  federation match[] + recording rules          /apps/{app}/path             │
                          │                                      /impact, /interfaces/.../apps│
                          ▼                                              ▼                     │
   main Prometheus ── federate slice ──► per-app tenant          Grafana datasource plugin    │
     (no app label, bounded)              (app-labeled series) ── remote_write ──► long-term ◄┘
                                                                                    storage
```

Two layers sit on top of the graph:

1. **The graph (always on).** Identity-resolved nodes for applications, services, customers, devices, interfaces and IPs, joined by directional connections and ordered paths. Every fact carries provenance and a validity interval. This answers impact and path queries for *every* application, including the parts of the network with no flow coverage.

2. **Per-application projection (opt-in, curated).** For applications that need first-class metrics (`sum by(app)`, SLOs, alerting), the enrichment generator emits two GitOps artifacts per application: a Prometheus federation selector that pulls only that application's slice of series, and a set of recording rules that re-expose those series with `app`/`service` labels. The main Prometheus stays untouched.

The result: the heavy infrastructure metrics keep their existing, bounded cardinality, and the application context is either a graph lookup (free) or a small, intentional set of derived series.

---

## Data model

The graph is a property graph (Neo4j). Every node carries a deterministic identity hash (`phash`) so the same entity, seen as an IP, an FQDN, a CMDB sys_id, a serial number, or an inventory id, collapses to one node instead of forking the graph.

**Business layer**

```
(:Customer)-[:CONSUMES]->(:BusinessService)
(:BusinessService)-[:REALIZED_BY]->(:ApplicationService)
(:Application)-[:RUNS_AS]->(:ApplicationService)
```

**Connectivity layer** (reified, directional)

```
(:ApplicationService)-[:USES|:EXPOSES]->(:Endpoint)
(:Connection)-[:FROM]->(:Endpoint)        // client / source
(:Connection)-[:TO]->(:Endpoint)          // server / destination
(:ApplicationService)-[:DEPENDS_ON]->(:ApplicationService)
```

**Physical-path layer** (ordered, the anchor for path-health)

```
(:Connection)-[:TAKES]->(:Path)           // one connection may take many CANDIDATE paths
(:Path)-[:HOP {seq, direction}]->(:Interface)
(:Interface)-[:ON]->(:Device)
```

Notes that shape the whole system:

- **Interfaces are keyed by `(device, canonical ifName)`**, not by `ifIndex`. `ifIndex` renumbers on reconfiguration and is stored as a refreshable attribute, resolved to its current value when rules are generated.
- **Multiple candidate paths.** ECMP, multihop and alternate routing are modelled as several candidate `Path`s under one `Connection`. Which path is active, and its weight, are runtime facts that are *not* declared — they are filled in later from flow data.
- **Provenance and time on every fact.** Each edge carries `{provenance, confidence, source, valid_from, valid_to}`. The current state is everything with `valid_to` unset; a point-in-time query filters the interval, so you can ask *who was affected last Tuesday*.

---

## Requirements

| Component | Needed for | Notes |
|-----------|-----------|-------|
| Go 1.26+ | Building everything | Single static binaries |
| Neo4j 5 | The graph store | Memgraph also works (both speak Cypher) |
| Prometheus | Interface catalog + federation | Any version exposing `ifHC*Octets` with `ifName`/`ifDescr`/`ifAlias`/`ifIndex` labels |
| Nautobot | Optional | Device → management-IP (`instance`) mapping, seed |
| ServiceNow | Optional | Seed applications and services |
| Grafana 10.4+ | The datasource plugin | Enterprise supported |
| Docker or Podman | Integration tests only | The test suite spins up throwaway Neo4j containers |

---

## Quickstart

This walks an application from a YAML declaration all the way to a queryable path, using a local Neo4j.

```bash
# 1. Build the binaries
make build          # or: go build ./...

# 2. Start a graph (any Neo4j 5 works; here, a throwaway container)
docker run -d --name neo4j -p7687:7687 -p7474:7474 \
  -e NEO4J_AUTH=neo4j/changeme neo4j:5.23

# secrets come from the environment (NEO4J_PASS / SERVICENOW_PASS / NAUTOBOT_TOKEN); never pass them as flags
export NEO=bolt://localhost:7687
export NEO4J_PASS=changeme
export SERVICENOW_PASS=<your-password>
export NAUTOBOT_TOKEN=<your-token>

# 3. (Optional) seed application/service nodes from ServiceNow
./promhash-seed -neo4j $NEO \
  -servicenow https://example.service-now.com \
  -servicenow-user "$SN_USER"

# 4. Sync the interface catalog from Prometheus (and Nautobot for device names)
./promhash-catalog -neo4j $NEO \
  -prometheus http://prometheus:9090 \
  -nautobot https://nautobot.example.com \
  -vendor cisco

# 5. Write a declaration (see "Declaring application paths") into ./declared/payments.yaml,
#    then validate it (this is what CI runs on a pull request)
./promhash-loader -dir ./declared -neo4j $NEO -validate-only

# 6. Load it into the graph
./promhash-loader -dir ./declared -neo4j $NEO -source "$(git rev-parse HEAD)"

# 7. Generate federation + recording-rule artifacts for curated apps
./promhash-enrich -neo4j $NEO -apps payments -out ./gitops/enrichment

# 8. Serve the graph
./promhash-api -neo4j $NEO -addr :8080 &

# 9. Ask the questions
curl -s localhost:8080/apps
curl -s localhost:8080/apps/payments/path | jq
curl -s "localhost:8080/impact?device=rtr-core-1&ifName=tengige0/1/2" | jq
```

---

## Declaring application paths

Application paths are declared as YAML in a git repository. The git history *is* the record of declared facts: a pull request is a data review, and the commit SHA becomes the provenance `source`. A declaration is intentionally small; the loader expands it into the full graph model.

```yaml
# declared/payments.yaml
app: payments                 # -> (:Application)
runs_as: payments-api         # -> (:ApplicationService), RUNS_AS
owner: team-payments

consumed_by_customers:        # -> (:Customer)-CONSUMES->(:BusinessService)-REALIZED_BY->payments-api
  - acme
  - globex

depends_on:                   # each entry -> DEPENDS_ON + a (:Connection) with candidate (:Path)s
  - to: ledger-api
    paths:                    # candidate set — no active/standby, no weight (those are runtime facts)
      - hops:
          - {device: rtr-acc-fra-1, if: Te0/1/2,            direction: egress}
          - {device: rtr-core-1,    if: "uplink-ledger-dc", direction: transit}   # matched by ifAlias
          - {device: rtr-dc-ledger, if: Te1/0/4,            direction: ingress}
      - hops:                 # an alternate / ECMP-member candidate
          - {device: rtr-acc-fra-1, if: Te0/1/2, direction: egress}
          - {device: rtr-core-2,    if: Te0/2/1, direction: transit}
          - {device: rtr-dc-ledger, if: Te1/0/4, direction: ingress}

  - to: auth-api
    path:                     # `path:` is shorthand for a single-candidate `paths:` list
      hops:
        - {device: rtr-acc-fra-1, if: Te0/1/2, direction: egress}
        - {device: fw-dmz-1,      if: eth2,    direction: transit}
        - {device: rtr-dc-auth,   if: Te0/0/1, direction: ingress}
```

Field reference:

| Field | Meaning |
|-------|---------|
| `app` | Business application name. |
| `runs_as` | The application service that actually carries traffic. |
| `owner` | Owning team; surfaced in impact results. |
| `consumed_by_customers` | Customers/tenants that consume the application; gives the customer dimension in impact. |
| `depends_on[].to` | A downstream application service this one talks to (east-west dependency). |
| `depends_on[].paths[].hops[]` | An ordered list of hops for one candidate path. |
| `hop.device` | Device name (must resolve via the catalog). |
| `hop.if` | A *human* interface reference. Matched against the real `ifName`, `ifDescr`, or `ifAlias` in Prometheus, in any vendor syntax (`Gi0/3`, `GigabitEthernet0/3`, `ge-0/0/3`, an alias such as `uplink-ledger-dc`, …). |
| `hop.direction` | `egress`, `transit`, or `ingress`, relative to traffic flow. Selects `ifHCOutOctets` vs `ifHCInOctets` during enrichment. |

**Interface references are validated against reality.** On a pull request, `promhash-loader -validate-only` resolves every `device`/`if` pair against the catalog (which is harvested from live Prometheus metrics). An unknown or ambiguous reference fails the check with a "did you mean …" suggestion list, so a broken declaration never reaches the graph. Removing an entry from the YAML closes its validity interval rather than hard-deleting it, preserving point-in-time history.

---

## Command-line tools

All tools share the Neo4j connection flags `-neo4j` and `-neo4j-user`.

**Secrets.** To keep credentials out of process listings, each tool reads its secret from the environment: `NEO4J_PASS` (all tools), `NAUTOBOT_TOKEN` (`promhash-catalog`), and `SERVICENOW_PASS` (`promhash-seed`). Always use the environment variables; never pass secrets as flags.

### `promhash-catalog` — build the interface catalog

Harvests the real interface inventory from Prometheus and binds it to device names from Nautobot, upserting `Interface` nodes that carry the actual metric labels and current `ifIndex`. This is the normalization layer that lets declarations use human interface names. Run it on a schedule.

```
-prometheus      Prometheus base URL                 (default http://localhost:9090)
-nautobot        Nautobot base URL                   (optional; enables device<->instance mapping)
-vendor          default vendor for name canonicalization  (default cisco)
```

Nautobot authentication token: set `NAUTOBOT_TOKEN` in the environment.

### `promhash-seed` — import typed nodes from ServiceNow

One-shot import of application and application-service nodes so declarations resolve against real entities.

```
-servicenow       ServiceNow base URL
-servicenow-user  username
```

ServiceNow password: set `SERVICENOW_PASS` in the environment.

### `promhash-loader` — validate and load declarations

Reads every `*.yaml` in a directory, resolves interface references, and loads the declarations into the graph. Use `-validate-only` as a CI gate.

```
-dir            directory of declaration YAML files  (default declared)
-source         provenance source, e.g. the git SHA  (default manual)
-validate-only  validate against the catalog and exit non-zero on any error; write nothing
```

### `promhash-enrich` — generate GitOps artifacts for curated apps

For each named application, traverses the graph, resolves current `ifIndex`/`instance`, and writes a federation `match[]` selector and a recording-rule group under `-out/<app>/`.

```
-apps  comma-separated list of curated application names
-out   output directory for artifacts  (default gitops/enrichment)
```

### `promhash-api` — serve the graph over HTTP

```
-addr  listen address  (default 127.0.0.1:8080 — bind to localhost; front with an authenticating proxy to expose)
```

---

## HTTP API

A small read-only surface over the graph, backed by Cypher. It is the single server-side entry point for the Grafana plugin, alert enrichment, and any other consumer. It returns JSON and adds no cardinality to Prometheus.

| Endpoint | Returns |
|----------|---------|
| `GET /apps` | List of application names. |
| `GET /apps/{app}/path` | The application's path as an ordered list of hops. |
| `GET /interface-apps?device=&ifName=` | Applications, services and customers that traverse an interface. |
| `GET /impact?device=&ifName=&at=<unix>` | Blast radius for an interface, optionally at a point in time. |

`{app}` is a business application name. `device` and `ifName` are passed as query parameters using the canonical interface name (the form stored in the catalog) — query parameters are used because canonical names contain `/`. The optional `at` parameter is a Unix timestamp for point-in-time queries; it defaults to now.

Example — application path:

```bash
curl -s localhost:8080/apps/payments/path | jq
```
```json
[
  {"device":"rtr-acc-fra-1","ifName":"tengige0/1/2","metricIfName":"Te0/1/2",
   "instance":"10.0.0.5","direction":"egress","ifIndex":7,"seq":1,"provenance":"declared","confidence":0},
  {"device":"rtr-core-1","ifName":"tengige0/1/2","metricIfName":"Te0/1/2",
   "instance":"10.0.0.1","direction":"transit","ifIndex":42,"seq":2,"provenance":"declared","confidence":0}
]
```

Example — impact:

```bash
curl -s "localhost:8080/impact?device=rtr-core-1&ifName=tengige0/1/2" | jq
```
```json
{"interface":"rtr-core-1/tengige0/1/2",
 "impact":[{"app":"payments","service":"payments-api","customer":"acme","owner":"team-payments","criticality":"tier-1"}]}
```

When an interface has no known path, the impact endpoint returns an explicit `"note":"no path known"` rather than an empty result that could be mistaken for "nothing is affected".

---

## Grafana datasource plugin

A first-class Grafana datasource (`plugin/promhash-datasource`) that surfaces the graph through a proper query editor, template variables, and alerting. It is a thin adapter over the HTTP API — it does not talk to Neo4j directly, so all graph logic stays server-side.

**Configuration:** one setting, the promhash API URL.

**Query types:**

- `app_path` — an application's ordered hops, for path-health panels.
- `impact` / `interface_apps` — the applications affected by an interface.

**Variable queries** (via the plugin's resource endpoint):

- `apps` — populate an application picker.
- `path_interfaces/<app>` — the interface set for the selected application.

**The zero-cardinality dashboard pattern.** For applications that are *not* projected into per-app series, a dashboard joins two datasources at query time: a plugin template variable supplies *which* interfaces an application crosses, and a normal Prometheus panel queries the raw infrastructure metrics scoped to that set:

```promql
rate(ifHCOutOctets{instance=~"$instance", ifIndex=~"$ifIndex"}[5m])
```

The application-to-interface mapping becomes a Grafana variable, never a label. Curated applications skip the plugin entirely and query their `app:…` series directly with `sum by(app)`.

For Grafana Enterprise the plugin is built and privately signed (or allow-listed) before deployment. See `plugin/promhash-datasource/README.md` for build and signing steps.

---

## Enrichment and federation

For curated applications, `promhash-enrich` writes two artifacts per application:

- **`federate.match`** — a Prometheus `match[]` selector listing exactly that application's interfaces, e.g.
  `{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1|10.0.0.2", ifIndex=~"42|43"}`
- **`rules.yaml`** — a recording-rule group, one rule per hop (no summation across candidate paths, which would double-count), direction-aware, stamping `app`, `service`, `device`, `ifName`, and `coverage`.

A per-application tenant Prometheus federates only its slice from the main Prometheus, evaluates the recording rules, and remote-writes the resulting `app`-labeled series to long-term storage. The generator also emits a tenant scrape-config via `internal/enrich`'s `TenantScrapeConfig`. The existing GitOps pipeline applies everything under `gitops/enrichment/<app>/`.

The working set per tenant is the size of one application's path — tens to hundreds of series — so the recording rules are cheap to evaluate. A core link shared by several curated applications is recorded once per tenant; that duplication is bounded by the (small, intentional) curated set.

---

## How-to recipes

**"Which applications and customers cross this interface?"**
```bash
curl -s "localhost:8080/interface-apps?device=rtr-core-1&ifName=tengige0/1/2" | jq
```

**Build an app-path-health dashboard (no per-app series).** Add the promhash datasource; create a variable from `path_interfaces/$app`; in a Prometheus panel, filter the raw interface metrics by that variable.

**Enrich an application into first-class metrics.** Add the application name to the `promhash-enrich -apps` list, run it, and let GitOps deploy the generated `gitops/enrichment/<app>/` artifacts to its tenant. Then query `app:if_egress_octets:rate5m{app="<app>"}`.

**Ask who was affected at a past time.** Pass a Unix timestamp:
```bash
curl -s "localhost:8080/impact?device=rtr-core-1&ifName=tengige0/1/2&at=1700000100" | jq
```

**Keep the catalog fresh.** Schedule `promhash-catalog` (for example, every few minutes). It re-reads the live interface labels so renamed or renumbered interfaces are picked up before the next enrichment run.

---

## Codebase reference

A Go workspace: command entry points under `cmd/`, the libraries under `internal/`, and the Grafana plugin as a separate module under `plugin/`.

| Package | Responsibility | Key API |
|---------|---------------|---------|
| `internal/phash` | Deterministic identity hashing. Normalizes and hashes the keys that identify each kind of node so duplicates across sources collapse. | `Hash(kind Kind, parts ...string) string` |
| `internal/graph` | The Neo4j access layer: schema constraints, node/edge upserts, and the path/impact traversals. | `Repo`, `New`, `EnsureConstraints`, `UpsertInterface`, `AppPath`, `InterfaceImpact`, `ListApps`, `UpsertDeclaredApp`, `CloseAppValidity` |
| `internal/catalog` | The interface catalog and resolver: vendor name canonicalization, matching a human interface reference to exactly one real interface, and syncing harvested interfaces into the graph. | `CanonicalIfName`, `Resolver`, `NewResolver`, `(*Resolver).Resolve`, `Sync` |
| `internal/declare` | The declared-path YAML format, parsing, validation against the catalog, and loading into the graph. | `App`, `Parse`, `Validate`, `Load`, `(Dependency).Candidates` |
| `internal/enrich` | Artifact generation: federation selectors, recording-rule groups, and tenant scrape-configs. | `FederationMatch`, `RuleGroup`, `TenantScrapeConfig` |
| `internal/promclient` | Prometheus query client used to harvest the live interface inventory. | `Client`, `New`, `HarvestInterfaces` |
| `internal/nautobot` | Nautobot client; maps device names to management IPs (the Prometheus `instance`). | `Client`, `New`, `DeviceInstanceMap` |
| `internal/servicenow` | ServiceNow client; reads applications and services for seeding. | `Client`, `New`, `Applications` |
| `internal/api` | The HTTP server and handlers over a graph-repo interface. | `Server`, `NewServer`, `(*Server).Mux` |
| `internal/testutil` | A Neo4j testcontainer helper used by the integration tests. | `Neo4j` |
| `cmd/*` | The five command-line entry points: `promhash-catalog`, `promhash-seed`, `promhash-loader`, `promhash-enrich`, `promhash-api`. | — |
| `plugin/promhash-datasource` | The Grafana datasource plugin (Go backend + React frontend). | `Datasource`, `NewDatasource`, `QueryData`, `CallResource`, `CheckHealth` |

Browse the full per-package reference with Go's documentation tool:

```bash
go doc ./internal/graph
go doc ./internal/catalog Resolver
```

---

## Development

```bash
make build      # build all packages
make test       # unit tests (fast, no containers)
make test-int   # integration tests (spin up Neo4j containers)
make lint       # go vet
```

**Testing approach.** Pure logic (identity hashing, name canonicalization, reference resolution, selector and rule generation, HTTP handlers) is covered by fast unit tests with no external dependencies. Anything that touches Neo4j is covered by integration tests tagged `//go:build integration`, which start a throwaway Neo4j container per package. Those tests need a container engine:

```bash
# Docker
make test-int

# Podman (Docker-compatible socket; disable the resource reaper)
TESTCONTAINERS_RYUK_DISABLED=true make test-int
```

---

## Design principles

- **Infrastructure metrics never gain an `app` dimension.** This is a permanent rule. Application context lives in the graph; per-application series are a separate, small, curated family.
- **Honest provenance.** Every fact records how it was derived and a confidence, so a dashboard can tell declared from observed and never presents an estimate as a measurement.
- **Temporal by default.** Facts have validity intervals, so impact and path queries can be answered for any point in time.
- **Git is the source of truth for declared data.** The graph is a queryable materialization; the YAML in version control is the record, with review and history for free.
- **Curated-only projection.** Per-application series exist only where they earn their keep. Everything else is a free graph lookup.

---

## Known v1 simplifications

The data model in the graph is the full design; v1 deliberately implements a subset of it. These are conscious scope decisions, not oversights, and the schema is built so each can be filled in without a rewrite:

- **Endpoint / IP / Segment nodes are not yet materialized.** Declared data has no `ip:port`, so the loader wires `ApplicationService -[:USES]-> Connection` directly. The `ip:port:proto` Endpoint layer (and `Connection -[:FROM|:TO]-> Endpoint` directionality) lands with flow ingestion (Layer 2); until then traffic direction is carried per-hop on `HOP`.
- **Device identity is a property, not a node.** An interface stores its device name as the `device` property (sufficient for the impact and path queries); a first-class `(:Device)` node with `(:Interface)-[:ON]->(:Device)` is deferred.
- **`HOP` validity is inherited from its parent `TAKES` edge.** Point-in-time queries filter on `TAKES` validity; `HOP` carries only `seq` and `direction`. Superseded `Path` nodes are retained immutably as history and are reachable only through a closed `TAKES`.
- **The HTTP API ships no built-in authentication or TLS.** It binds to localhost by default and returns generic errors. Expose it by fronting it with an authenticating reverse proxy or mTLS; treat the API host as a trust boundary.

## Roadmap

The data model is built to absorb these without a rewrite:

- **Quantitative attribution (Layer 2).** Ingest flow records (NetFlow/IPFIX from the core, firewall byte counts at boundaries) to split interface traffic across applications by share, and to fill in which candidate path was actually active and with what weight — written back as a `flow`-provenance overlay.
- **Automated path discovery.** Derive path edges from observed flow and from modeled topology/routing, alongside the declared edges, each with its own provenance.
- **Chargeback and per-customer views** once quantitative attribution exists.

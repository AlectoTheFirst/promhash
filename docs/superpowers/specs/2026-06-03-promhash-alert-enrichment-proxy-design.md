# promhash — Alert-Enrichment Proxy (Design)

- **Date:** 2026-06-03
- **Status:** Approved for planning
- **Author:** brainstormed with Claude Code
- **Builds on:** [2026-05-30 network-to-app mapping design](2026-05-30-promhash-network-to-app-mapping-design.md) (§C7 already names "Alertmanager enrichment" as a consumer of the graph API)

## 1. Problem

The promhash graph already answers "which apps/services/customers does this interface
carry?" via `Repo.InterfaceImpact` and the C7 API `GET /impact`. But that knowledge is
not yet on the alerts operators actually see. When a network alert fires (interface down,
high error rate, link saturation), Alertmanager and its notifications show only the
infrastructure identity (`instance`, `ifIndex`, device) — not the business blast radius.

We want firing alerts enriched, in-flight, with the affected applications, services,
owners, customers, and criticality pulled live from the graph, so the impact is visible in
Alertmanager and every notification it sends — and so Alertmanager can *route* on blast
radius (e.g. page the SRE on-call when a critical app is affected).

This feature is **independent** of the existing build-time enrichment (`internal/enrich`,
`cmd/promhash-enrich`), which generates GitOps federation/recording-rule artifacts. That
path is unchanged. This is a new runtime component.

## 2. Decisions (settled in brainstorming)

1. **Placement — inline push proxy.** Prometheus sends alerts to the proxy
   (`alertmanagers:` points at it); the proxy enriches and forwards to the real
   Alertmanager. Enrichment lands *before* AM routing/dedup/notify, so enriched fields
   drive routing and reach every notifier for free. The proxy is on the critical delivery
   path and therefore **fails open** (forwards un-enriched on any error).
2. **Attachment — annotations + bounded derived labels.** The full impact list goes in
   *annotations* (free-form, no routing/dedup impact). A small set of *bounded scalar*
   labels (max criticality, app-count, customer-impact bool) is added so Alertmanager can
   route/escalate. The high-cardinality app set is **never** stamped as a label — the
   project's cardinality law holds.
3. **Correlation — configurable label map, default exact `(instance, ifIndex)` with name
   fallback.** Proxy config declares which alert labels carry the device and interface
   keys. Default to exact `(instance, ifIndex)` match (most reliable for SNMP); fall back
   to the existing fuzzy name resolver when only names are present; forward un-enriched
   when neither resolves.
4. **Graph access — via the C7 promhash-api.** The proxy is a thin HTTP client of
   `promhash-api`; graph/Cypher logic stays server-side (design §C7). The proxy holds no
   Neo4j credentials. A new exact `(instance, ifIndex)` impact endpoint is added to the
   API.

## 3. Non-Goals

- Replacing Alertmanager's routing, grouping, templating, or notifier integrations. The
  proxy only mutates alert labels/annotations in flight.
- Inventing new graph data. Enrichment is a pure read of the existing impact traversal.
- High-cardinality `app=` labels on alerts (forbidden — same cardinality law as the parent
  design).
- Changing the existing build-time `internal/enrich` GitOps generator.
- Quantitative byte attribution / per-app weighting (Layer 2, out of scope upstream too).

## 4. Architecture

```
Prometheus ──POST /api/v2/alerts──► [promhash-alert-proxy] ──HTTP /impact──► promhash-api ──Bolt──► Neo4j
                                          │ enriched array
                                          └──POST /api/v2/alerts──► Alertmanager cluster ─► Slack / email / PD
```

The proxy is stateless. Prometheus may be configured with several proxy instances (it
sends to all configured alertmanagers for redundancy); each proxy fans the enriched batch
out to all configured upstream Alertmanager peers.

## 5. Components

Each unit has one purpose, a defined interface, and is testable in isolation.

### `cmd/promhash-alert-proxy/main.go`
- **Purpose:** wiring only — load config, build the `ImpactClient`, construct the proxy
  handler, serve HTTP (alert endpoint + `/metrics`), graceful shutdown.
- **Depends on:** `internal/alertenrich`.

### `internal/alertenrich/payload.go`
- **Purpose:** parse and serialize the Alertmanager v2 alert array. Faithfully preserve
  every field of every alert (`labels`, `annotations`, `startsAt`, `endsAt`,
  `generatorURL`, and any unknown fields) — only `labels`/`annotations` are mutated.
- **Interface:** `ParseAlerts([]byte) ([]Alert, error)`, `Marshal([]Alert) ([]byte, error)`.
  `Alert.Labels` / `Alert.Annotations` are `map[string]string`; unknown top-level fields
  are retained via `json.RawMessage` passthrough so the upstream payload is byte-faithful
  except for the two maps.

### `internal/alertenrich/correlate.go`
- **Purpose:** turn one alert + the configured label map into a correlation key.
- **Interface:** `Correlate(labels map[string]string, cfg LabelMap) (Key, bool)`. `Key` is
  one of: exact `{Instance, IfIndex}`, fuzzy `{Device, IfName}`, or none. Returns
  `ok=false` when no usable key is present (caller then forwards un-enriched).
- **Depends on:** nothing (pure).

### `internal/alertenrich/render.go`
- **Purpose:** turn `[]graph.ImpactRow` into the labels and annotations to attach. **Pure
  and golden-file tested**, mirroring `internal/enrich/rules.go`.
- **Interface:**
  `Render(rows []graph.ImpactRow, cfg RenderCfg) (labels, annotations map[string]string)`.
- **Output (prefix configurable, default `promhash_`):**
  - Labels (only when `enrich_labels` true):
    `promhash_max_criticality`, `promhash_app_count`, `promhash_customer_impact`.
  - Annotations: `promhash_impact` (multi-line list of app · service · owner · customer ·
    criticality), `promhash_blast_radius` (summary, e.g. `3 apps, 2 customers`).
  - Empty `rows` → no labels, no annotations (the alert passes through unchanged; nothing
    falsely reads as "no impact").

### `internal/alertenrich/client.go`
- **Purpose:** the proxy's view of the graph. An interface so the HTTP client is mockable
  and the proxy never imports Neo4j.
- **Interface:**
  ```go
  type ImpactClient interface {
      ImpactByInstanceIndex(ctx, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error)
      ImpactByName(ctx, device, ifName string, at time.Time) ([]graph.ImpactRow, error)
  }
  ```
- **Default impl:** `apiClient` calling C7 `GET /impact` with the appropriate params. A
  `*catalog.NoMatchError`/`*catalog.AmbiguousError`-equivalent HTTP response (404/409) maps
  to "no impact" (empty rows), not a hard error — so an unmatched interface fails open.

### `internal/alertenrich/proxy.go`
- **Purpose:** the HTTP handler. Receive the alert array, enrich each alert (correlate →
  client lookup → render → merge), forward the batch to all upstream Alertmanagers.
- **Behavior:** see §6 (correlation) and §8 (reliability).
- **Depends on:** `payload`, `correlate`, `render`, `client`.

### `internal/api` (extend, not new)
- Extend the existing `/impact` handler so that when `instance` and `ifIndex` query params
  are present it uses the exact lookup; otherwise the existing `device`+`ifName` fuzzy path
  is unchanged. Existing callers (the Grafana plugin's `/interface-apps`) are unaffected.

### `internal/graph` (extend)
- New `Repo.InterfaceImpactByInstanceIndex(ctx, instance string, ifIndex int, at) ([]ImpactRow, error)`.

## 6. Correlation (per alert, fail-open)

Resolve order, first hit wins:

1. **Exact:** `(alert[device_label], alert[iface_index_label])` →
   `ImpactByInstanceIndex`. (Default `device_label=instance`, `iface_index_label=ifIndex`.)
2. **Fuzzy fallback:** `(alert[device_label], alert[iface_name_label])` → `ImpactByName`
   (the existing catalog resolver). Used when the index label is absent.
3. **No key / no match / error / timeout:** forward the alert unchanged; increment the
   passthrough/failure counter; log at debug with the alert's identifying labels.

The point-in-time `at` is the alert's `startsAt` when present (topology as-of fire time),
else now.

## 7. Enrichment output (example)

Given an alert with `instance="10.0.0.1:161"`, `ifIndex="42"` whose interface carries
`payments` (customer `acme`, critical) and `ledger`:

```yaml
labels:
  # ...original labels unchanged...
  promhash_max_criticality: critical
  promhash_app_count: "2"
  promhash_customer_impact: "true"
annotations:
  # ...original annotations unchanged...
  promhash_impact: |
    apps affected (2):
    - payments (payments-api) owner team-payments customer acme [critical]
    - ledger   (ledger-api)   owner team-ledger
  promhash_blast_radius: "2 apps, 1 customer"
```

Merge rule: promhash keys overwrite any pre-existing same-named keys (deterministic); all
other labels/annotations are preserved verbatim.

## 8. Reliability, dedup, and HA

### Fail-open (mandatory — the proxy is on the delivery path)
- Any error in correlation, lookup, render, or marshalling of *one* alert → that alert is
  forwarded **unchanged**. Other alerts in the same batch are still enriched.
- Per-alert lookup timeout (default 2s) via context deadline; on timeout, forward
  un-enriched.
- The handler always returns 200 to Prometheus once the batch has been forwarded upstream.
- If *forwarding* to upstream fails for all AM peers, return 5xx so Prometheus retries
  (alerts must not be silently dropped).

### Alertmanager fingerprint tradeoff (known, documented)
Alertmanager dedups/groups alerts by their **label fingerprint**. Adding labels changes
the fingerprint. This is **safe while derived labels are deterministic for a given alert**:
re-sends and the final resolved alert carry the same original labels → the proxy computes
the same derived labels → same fingerprint → the alert resolves cleanly.

**Resolved-alert coupling (correctness, not optional):** because Alertmanager correlates a
firing alert with its resolved counterpart by fingerprint, derived labels added to a firing
alert **must** also be added to the matching resolved alert, or the alert never clears.
Therefore: whenever `enrich_labels` is true, the proxy applies the derived labels to **both
firing and resolved** alerts unconditionally. `enrich_resolved` governs only whether the
*annotations* (cosmetic) are added to resolved alerts — it never gates the labels. A
combination of `enrich_labels:true` + `enrich_resolved:false` is valid and means "labels on
both, impact annotation on firing only".

**Risk:** if topology changes *mid-incident* (an app is added to / removed from the
interface's path, flipping `promhash_app_count` or `promhash_max_criticality`), the
fingerprint changes and Alertmanager treats it as a new alert; the prior one resolves only
on its timeout. This is rare (declared topology changes are PR-gated and infrequent
relative to an alert's lifetime). Mitigations, both in scope:
- The derived label set is intentionally coarse and slow-changing.
- `enrich_labels: false` moves **everything** into annotations (which never affect the
  fingerprint), trading routing-on-blast-radius for zero dedup risk. Operators choose.

### HA / scaling
- The proxy is stateless → horizontally scalable.
- Prometheus is configured with all proxy instances; it sends each batch to all of them for
  redundancy (Prometheus' normal AM behavior).
- Each proxy fans the enriched batch out to **all** configured upstream Alertmanager peers
  (preserving AM gossip dedup), and considers forwarding successful if **≥1** peer accepts.

### Observability (`/metrics`, Prometheus format)
- `promhash_alert_proxy_alerts_received_total`
- `promhash_alert_proxy_alerts_enriched_total`
- `promhash_alert_proxy_alerts_passthrough_total{reason="no_key|no_match|error|timeout"}`
- `promhash_alert_proxy_lookup_seconds` (histogram)
- `promhash_alert_proxy_forward_errors_total{upstream}`

## 9. Configuration

Flags with env fallback (mirrors the existing cmd style, e.g. `promhash-enrich`):

```yaml
listen: ":9094"                                  # alert intake + /metrics
upstream_alertmanagers: ["http://am-0:9093", "http://am-1:9093"]
promhash_api: "http://promhash-api:8080"
device_label: "instance"                         # alert label carrying device/target
iface_index_label: "ifIndex"                     # alert label carrying ifIndex (exact)
iface_name_label: "ifName"                        # alert label for the name fallback
lookup_timeout: "2s"
label_prefix: "promhash_"
enrich_labels: true                              # false => annotations-only (fingerprint-safe)
enrich_resolved: true                            # add impact ANNOTATIONS to resolved alerts too.
                                                 # NOTE: derived labels are always applied to
                                                 # resolved alerts when enrich_labels is true,
                                                 # regardless of this flag (fingerprint match, §8).
```

The alert intake path matches Alertmanager's so Prometheus needs no special config beyond
pointing `alertmanagers:` at the proxy: `POST /api/v2/alerts`.

## 10. Failure modes & handling

| Failure | Handling |
|---|---|
| Graph/API down or 5xx | Per-alert lookup error → forward un-enriched; count `passthrough{reason=error}`. |
| Lookup exceeds timeout | Forward un-enriched; count `passthrough{reason=timeout}`. |
| Interface not in graph (404) | Treated as empty impact → no enrichment, alert passes through; `passthrough{reason=no_match}`. |
| Ambiguous name match (409) | Treated as no match → un-enriched; logged with suggestions. |
| Alert lacks correlation labels | `passthrough{reason=no_key}`; un-enriched. |
| Malformed alert JSON from Prometheus | 400 to Prometheus (its payload is wrong); nothing forwarded. |
| All upstream AMs unreachable | 5xx to Prometheus so it retries; alerts never dropped. |
| Some upstream AMs unreachable | Success if ≥1 accepted; count `forward_errors{upstream}`. |
| Topology change mid-incident | Documented dedup tradeoff (§8); mitigated by coarse labels / `enrich_labels:false`. |

## 11. Testing strategy

- **`render.go`** — golden-file table tests: empty impact (no output), single app,
  multi-app, multi-customer, criticality ordering (max selection), customer-impact bool,
  configurable prefix, `enrich_labels:false` (annotations only). Pattern mirrors
  `internal/enrich/testdata/*.golden.yaml`.
- **`correlate.go`** — table tests: exact hit, fuzzy fallback when index absent, no key,
  custom label map.
- **`payload.go`** — round-trip a real AM v2 batch; assert untouched fields are
  byte-faithful and only the two maps change.
- **`proxy.go`** — `httptest`: happy-path enrich; fail-open on client 500 / timeout /
  ambiguous / no-match (assert original forwarded unchanged); fan-out to multiple upstream
  AMs (≥1 success); 5xx when all upstreams fail; resolved-alert handling — assert derived
  labels are present on resolved alerts when `enrich_labels:true` even with
  `enrich_resolved:false`, and that the annotation is gated by `enrich_resolved`. Uses a
  fake `ImpactClient`.
- **`internal/api`** — contract test for the exact `(instance, ifIndex)` `/impact` path on
  a seeded graph; assert existing `device`+`ifName` path unchanged.
- **`internal/graph`** — `InterfaceImpactByInstanceIndex` against ephemeral Neo4j (mirrors
  `impact_test.go`), including zero-match and the (shouldn't-happen) multi-match case.
- **End-to-end smoke** — POST a fixture alert batch at the proxy with a fake C7 API
  returning known impact; assert the forwarded batch carries the expected labels +
  annotations and that an un-correlatable alert in the same batch passes through.

## 12. Open items (defaulted; trivially flippable)

- `at` = `startsAt` (chosen) vs always `now`.
- Upstream fan-out to **all** AM peers (chosen) vs a single endpoint.
- Listen port `:9094` (chosen; distinct from AM's `:9093`).

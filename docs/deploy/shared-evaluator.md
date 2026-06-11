# Shared-evaluator deployment (remote-write receiver)

This document describes the **shared-evaluator projection model** — the
deployment architecture for generating per-app path-health metrics from a
single dedicated rule-evaluating Prometheus, the *promhash Prometheus*.

> **Supersedes:** `docs/deploy/federation-tenant.md` (the old per-app
> federation/tenant model). See the deprecation note in that file.

---

## Overview

The promhash Prometheus is a **remote-write receiver**: the existing main
Prometheus pushes the raw SNMP counters to it with one `remote_write` block —
the only promhash-related change ever made to the main Prometheus. The
promhash Prometheus scrapes the bounded mapping series, evaluates the
path-health recording and alerting rules once for all curated apps, and
remote-writes the resulting app-labeled series onward with a `tenant`
external label.

No federation, no per-application scrape, no series added to the main
Prometheus, and no exporter is ever scraped twice.

```
  snmp_exporter ── scrape ──► main Prometheus
                                   │
                                   │ remote_write (raw if* counters + ALERTS,
                                   │ optionally scoped by write_relabel_configs)
                                   ▼
  mapping server ── scrape ──► promhash Prometheus        ← evaluator.yaml
  (serves mapping.prom)          started with --web.enable-remote-write-receiver
                                 loads: path-health.rules.yaml      (recording)
                                        path-health.alerts.yaml     (alerting)
                                 joins: counters × mapping on(instance, ifName)
                                 emits: app:if_*, app:path_* series
                                   │
                                   │ remote_write (tenant external label)
                                   ▼
  long-term storage (Mimir / Thanos / VictoriaMetrics / …)
```

Why a receiver rather than a scraper or federation: remote_write delivers
samples with their labels **verbatim** — `instance`, `ifName`, `ifIndex`
arrive exactly as the main Prometheus stored them, so the rule joins work
without `honor_labels` tricks or relabeling. A vanilla Prometheus can only
evaluate rules over its own TSDB, and remote_write is the way to fill that
TSDB without scraping anything twice.

---

## Artifacts emitted by `promhash-enrich`

`promhash-enrich` writes four files under `_shared/`:

| File | Description |
|------|-------------|
| `_shared/mapping.prom` | Prometheus exposition text for `promhash_interface_app{…}=1`. One sample per (interface, app, direction) pair. |
| `_shared/path-health.rules.yaml` | The static `promhash_path_health` recording-rule group. App-independent; evaluated once for all apps. |
| `_shared/path-health.alerts.yaml` | The `promhash_path_health_alerts` alerting-rule group: pipeline meta-alerts + path alerts (see below). |
| `_shared/evaluator.yaml` | The promhash Prometheus config shell (`SharedEvaluatorConfig`). |

The GitOps pipeline delivers these files to the promhash Prometheus. No
per-app artifact directory is needed.

---

## Input metrics

The rules consume, via the remote_write feed:

| Metric | Used for |
|--------|----------|
| `ifHCInOctets` / `ifHCOutOctets` | per-hop ingress/egress rates (64-bit HC counters — mandatory for octets; the 32-bit variants wrap in seconds at 10G) |
| `ifHighSpeed` | capacity → utilization |
| `ifOperStatus` | up/down state, hops-down rollup |
| `ifInErrors` / `ifOutErrors` | error rates (no HC variants exist in IF-MIB; 32-bit is safe at error rates) |
| `ifInDiscards` / `ifOutDiscards` | discard rates (buffer/QoS drops — often the first degradation signal) |
| `ALERTS` (optional) | firing interface alerts fanned out to apps (`app:if_alerts_firing:count`) |

And, via its own scrape: `promhash_interface_app` (the mapping).

---

## The mapping series

`mapping.prom` contains `promhash_interface_app` samples with the full identity
label set:

```
promhash_interface_app{app="payments",service="payments-api",device="rtr-core-1",
  ifName="Te0/1/2",instance="10.0.0.1",ifIndex="42",iface="10.0.0.1:42",
  direction="egress"} 1
```

Labels carried on every sample:

| Label | Meaning |
|-------|---------|
| `app` | Business application name. |
| `service` | Application service. |
| `device` | Device name. |
| `ifName` | Canonical metric interface name. |
| `instance` | Prometheus scrape instance (`host:port` or IP). |
| `ifIndex` | Interface index at declaration time. |
| `iface` | Composite key `instance:ifIndex` (informational under the `ifname` join). |
| `direction` | `ingress` or `egress`. Transit interfaces expand to both. |

---

## Recording rules

`path-health.rules.yaml` is rendered by `PathHealthRules(jk)`. The rules use
`group_right()` because the mapping series is the RIGHT ("many") operand — a
single physical interface maps to multiple apps. The counter is the LEFT
("one") operand. See the README's "Enrichment and projection" section for the
full rule listing; the structural points:

- **Octet rates** join the raw mapping filtered per direction.
- **Capacity** joins the raw mapping unfiltered, yielding one series per
  mapped direction — this is what lets the two utilization rules (egress and
  ingress, sharing the record name `app:if_util:ratio`) divide one-to-one on
  the full label set. Ingress-declared hops get a utilization series.
- **Oper-up, errors, discards, alert counts** join the direction-collapsed
  mapping (`max without(direction)(…)`), so a transit interface is never
  double-counted.
- **`app:path_hops_down:count`** is `sum by(app, service)(1 - state)`: a
  fully-healthy path reads an explicit `0`. "No data" always means the
  pipeline is broken, never "all healthy".
- **`app:if_alerts_firing:count`** pre-collapses `ALERTS` with
  `count by(<join labels>)` because M alerts × N apps per interface is not
  expressible as a direct PromQL vector match.

---

## Alerting rules

`path-health.alerts.yaml` (`PathHealthAlerts(jk)`) carries two layers.

**Pipeline meta-alerts** — the projection's signature failure mode is silent
emptiness (everything runs, rules match nothing); each silent state pages:

| Alert | Fires when |
|-------|-----------|
| `PromhashMappingAbsent` | `promhash_interface_app` missing entirely (mapping file empty / never ingested). |
| `PromhashMappingScrapeDown` | the mapping scrape target is down. |
| `PromhashCountersStale` | newest `ifHCInOctets` sample older than 5 minutes — the remote_write feed from the main Prometheus stalled. |
| `PromhashMappingDrift` | mapping rows whose join key matches no counter series — interface renamed/renumbered/retired since the last enrich run. Re-run `promhash-catalog` + `promhash-enrich`. |

**Path alerts** — `PromhashPathHopDown` (annotated with the ECMP caveat: a
down hop may be a redundant candidate — redundancy lost, not necessarily an
outage), `PromhashPathUtilizationHigh` (worst hop >90% for 15m), and
`PromhashPathErrors` (sustained non-zero error rate).

Route them through your existing Alertmanager; promhash ships no Alertmanager
of its own.

---

## evaluator.yaml reference (`SharedEvaluatorConfig`)

```yaml
global:
  external_labels:
    tenant: <deployment-name>

rule_files:
  - path-health.rules.yaml
  - path-health.alerts.yaml

scrape_configs:
  - job_name: promhash-mapping
    honor_labels: true               # REQUIRED — see below
    metrics_path: /mapping.prom      # -mapping-path (default /mapping.prom)
    static_configs:
      - targets: [<mapping-server-host:port>]   # -mapping-target

remote_write:
  - url: <remote-write-url>          # onward, to long-term storage
```

There is **no counters scrape job and no relabel configuration**: the raw
counters arrive via remote_write, and a receiver never relabels remote-written
samples.

`honor_labels: true` on the mapping job is mandatory: the mapping exposition
carries the *devices'* `instance`/`ifIndex`/`iface` identity labels, and
without it Prometheus would rewrite `instance` to the mapping server's address
(moving the original to `exported_instance`), breaking the join and
mislabeling every `group_right()` result.

---

## Main Prometheus configuration

The one promhash-related change:

```yaml
remote_write:
  - url: http://<promhash-prom>:9090/api/v1/write
    # optional but recommended: ship only what the rules consume instead of
    # duplicating the whole estate's ingest
    write_relabel_configs:
      - source_labels: [__name__]
        regex: ifHCInOctets|ifHCOutOctets|ifHighSpeed|ifOperStatus|ifInErrors|ifOutErrors|ifInDiscards|ifOutDiscards|ALERTS
        action: keep
```

Include `ALERTS` if interface alerts evaluate on the main Prometheus and you
want them fanned out per app. To ship only interface-scoped alerts, fold the
`ifName` requirement into the keep regex instead:

```yaml
      - source_labels: [__name__, ifName]
        separator: ";"
        regex: "(ifHCInOctets|ifHCOutOctets|ifHighSpeed|ifOperStatus|ifInErrors|ifOutErrors|ifInDiscards|ifOutDiscards);.*|ALERTS;.+"
        action: keep
```

(counter metrics pass regardless of `ifName`; `ALERTS` passes only when it
carries a non-empty `ifName` label — or simply accept the extra alert series;
they are few).

---

## Join-key choice

| Join key | `on(...)` clause | When |
|----------|-----------------|------|
| `ifname` (default) | `on(instance, ifName)` | The receiver deployment. Counters carry `ifName` already (the catalog is harvested from those very labels), and a receiver cannot synthesize labels — so this is the only key that can work here. |
| `composite` | `on(iface)` | Only when the Prometheus that *scrapes* the exporters synthesizes `iface` (`instance:ifIndex`) at scrape time via `metric_relabel_configs` — the same relabel the zero-cardinality dashboard pattern needs. |

The join key is selected when running `promhash-enrich` and affects the rules
and the drift alert. Changing it requires regenerating and redeploying the
`_shared/` artifacts.

---

## Operational semantics

- **Remote_write lag.** The sender batches and retries; under backpressure
  delivery can lag minutes. Rules evaluate at wall-clock "now", so once the
  feed lags past the rate window the app series gap. `PromhashCountersStale`
  pages on exactly this.
- **Receiver downtime.** Raw samples backfill from the sender's WAL when the
  receiver returns, but rule results for the outage window are *not*
  re-evaluated — an app-series gap over the outage is expected semantics.
- **As-of-emit attribution.** Once app-labeled samples are remote-written to
  long-term storage they are immutable. The graph is retractable; LTS is not.
  Samples carry the mapping that was current when they were written, even if
  the graph is later corrected.

---

## Deploying

1. Run `promhash-enrich` with `-mapping-target`, `-remote-write-url` and
   `-tenant-label` (join key defaults to `ifname`) to generate the
   `_shared/` artifacts.
2. Serve `_shared/mapping.prom` over HTTP at the address given as
   `-mapping-target` (any static file server works — nginx, a GitOps
   sidecar).
3. Copy `_shared/path-health.rules.yaml`, `_shared/path-health.alerts.yaml`
   and `_shared/evaluator.yaml` to the promhash Prometheus config directory.
4. Start the promhash Prometheus with `--config.file=evaluator.yaml
   --web.enable-remote-write-receiver`.
5. Add the `remote_write` block above to the main Prometheus and reload it.
6. Confirm, in order: `promhash_interface_app` series present (mapping job
   up); `ifHCInOctets` present and fresh (remote_write feed flowing);
   `app:if_egress_octets:rate5m` present (the join matches); series arriving
   in long-term storage. The meta-alerts watch each of these from then on.

Regenerate and redeploy the `_shared/` artifacts whenever the curated app set
or any declared path changes, and after interface renames/renumbers
(`PromhashMappingDrift` tells you when this has been missed).

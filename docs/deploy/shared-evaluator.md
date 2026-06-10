# Shared-evaluator deployment

This document describes the **shared-evaluator projection model** — the deployment
architecture for generating per-app path-health metrics from a single
rule-evaluating Prometheus instance.

> **Supersedes:** `docs/deploy/federation-tenant.md` (the old per-app
> federation/tenant model). See the deprecation note in that file.

---

## Overview

A single rule-evaluating Prometheus (not agent mode — agent mode cannot evaluate
recording rules) scrapes the raw SNMP counter firehose, synthesizes the composite
`iface` label, evaluates the `promhash_path_health` recording rules, and
remote-writes the resulting app-labeled series once with a `tenant` external label.
No per-application scrape, federation hop, or separate tenant Prometheus is needed.

```
  snmp_exporter (raw counters)      mapping server (serves mapping.prom)
        │                                 │
        ▼                                 ▼ honor_labels: true
  shared evaluator Prometheus        ← evaluator.yaml (SharedEvaluatorConfig)
    scrapes raw counters + mapping
    relabels: iface = instance:ifIndex (composite mode, counters job only)
    loads: path-health.rules.yaml      ← PathHealthRules(JoinByComposite)
    joins: counters × mapping series   ← RenderMappingSeries
    emits: app:if_egress_octets:rate5m, app:if_oper_up:state, …
        │
        ▼ remote_write (tenant external label)
  long-term storage (Mimir / Thanos / VictoriaMetrics / …)
```

---

## Artifacts emitted by `promhash-enrich`

`promhash-enrich` writes three files under `_shared/`:

| File | Description |
|------|-------------|
| `_shared/mapping.prom` | Prometheus exposition text for `promhash_interface_app{…}=1`. One sample per (interface, app, direction) pair. |
| `_shared/path-health.rules.yaml` | The static `promhash_path_health` recording-rule group. App-independent; evaluated once for all apps. |
| `_shared/evaluator.yaml` | Rule-evaluating Prometheus config (`SharedEvaluatorConfig`). |

The GitOps pipeline delivers these three files to the shared evaluator. No per-app
artifact directory is needed.

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
| `iface` | Composite join key: `instance:ifIndex`. |
| `direction` | `ingress` or `egress`. Transit interfaces expand to both. |

---

## Path-health recording rules

`path-health.rules.yaml` is rendered by `PathHealthRules(JoinByComposite)`. The
rules use `group_right()` because the mapping series is the RIGHT ("many") operand
— a single physical interface maps to multiple apps, so the mapping must be the
many side or the join rejects duplicate matches. The counter series is the LEFT
("one") operand.

**Per-hop series** (one series per physical interface per mapped app):

```yaml
# Egress octet rate, app/service labels fanned on from the mapping:
- record: app:if_egress_octets:rate5m
  expr: rate(ifHCOutOctets[5m]) * on(iface) group_right() promhash_interface_app{direction="egress"}

# Ingress octet rate:
- record: app:if_ingress_octets:rate5m
  expr: rate(ifHCInOctets[5m]) * on(iface) group_right() promhash_interface_app{direction="ingress"}

# Interface capacity in bits/s (collapses direction multiplicity):
- record: app:if_capacity_bps
  expr: (ifHighSpeed > 0) * 1e6 * on(iface) group_right() max without(direction)(promhash_interface_app)

# Operational up-state as 0/1 gauge (direction-agnostic):
- record: app:if_oper_up:state
  expr: (ifOperStatus == bool 1) * on(iface) group_right() max without(direction)(promhash_interface_app)

# Link utilization ratio (ignoring direction label mismatch between numerator and denominator):
- record: app:if_util:ratio
  expr: app:if_egress_octets:rate5m * 8 / ignoring(direction) app:if_capacity_bps
```

**Per-path rollup series** (one series per `(app, service)` pair):

```yaml
- record: app:path_util_max:ratio
  expr: max by(app, service)(app:if_util:ratio)

- record: app:path_oper_up_min:state
  expr: min by(app, service)(app:if_oper_up:state)

- record: app:path_hops_down:count
  expr: count by(app, service)(app:if_oper_up:state == 0)
```

---

## evaluator.yaml reference (`SharedEvaluatorConfig`)

The generated `evaluator.yaml` has the following structure:

```yaml
global:
  external_labels:
    tenant: <deployment-name>

rule_files:
  - path-health.rules.yaml

scrape_configs:
  - job_name: promhash-evaluator
    static_configs:
      - targets: [<snmp-exporter-host:port>]
    metric_relabel_configs:          # JoinByComposite only
      - source_labels: [instance, ifIndex]
        separator: ":"
        target_label: iface
        regex: "(.+)"
        replacement: "${1}"

  - job_name: promhash-mapping
    honor_labels: true               # REQUIRED — see below
    metrics_path: /mapping.prom      # -mapping-path (default /mapping.prom)
    static_configs:
      - targets: [<mapping-server-host:port>]   # -mapping-target

remote_write:
  - url: <remote-write-url>
```

The `metric_relabel_configs` block synthesizes the composite `iface` label from
`instance` and `ifIndex` on every scraped counter sample. This is the join key
used by the `group_right()` recording rules.

The `promhash-mapping` job ingests the `promhash_interface_app` series — the
rules join against this metric, so **without this job every path-health rule
evaluates to empty**. `honor_labels: true` is mandatory: the mapping exposition
carries the *devices'* `instance`/`ifIndex`/`iface` identity labels, and without
it Prometheus would rewrite `instance` to the mapping server's address (moving
the original to `exported_instance`), breaking the `on(instance, ifName)` join
key and mislabeling every `group_right()` result. The mapping job carries no
`iface` relabel — the exposition text already contains an explicit `iface`
label.

---

## Join-key choice

| Join key | `on(...)` clause | Precondition |
|----------|-----------------|--------------|
| `composite` (default) | `on(iface)` | None. `iface` is synthesized by the evaluator from `instance:ifIndex`. |
| `ifname` | `on(instance, ifName)` | The `snmp_exporter` must expose `ifName` as a label on `ifHC*Octets` series. If your exporter configuration does not include `ifName` on counter metrics, use `composite`. |

The join key is selected when running `promhash-enrich` and affects all three
`_shared/` artifacts. Changing the join key requires regenerating and redeploying
all artifacts.

---

## As-of-emit LTS attribution

Once app-labeled samples are remote-written to long-term storage they are
immutable. The graph is retractable — removing an interface from an app's declared
path takes effect immediately in the graph — but long-term storage is not updated
retroactively. Attribution in LTS is **as-of-emit**: samples carry the app mapping
that was current when they were written, and that attribution persists even if the
graph mapping is later corrected. Keep this in mind when interpreting historical
path-health data across declaration changes.

---

## Deploying the shared evaluator

1. Run `promhash-enrich` with `-main-prom`, `-mapping-target`,
   `-remote-write-url` and `-tenant-label` to generate the `_shared/` artifacts.
2. Serve `_shared/mapping.prom` over HTTP at the address given as
   `-mapping-target` (any static file server works — nginx, `python -m
   http.server`, a GitOps sidecar). The generated `evaluator.yaml` already
   contains the `promhash-mapping` scrape job pointing at it.
3. Copy `_shared/path-health.rules.yaml` and `_shared/evaluator.yaml` to the
   evaluator's config directory.
4. Start (or reload) the evaluator Prometheus with `evaluator.yaml` as its
   `--config.file`.
5. Confirm that `promhash_interface_app` series appear in the evaluator (the
   mapping job is up), then that `app:if_egress_octets:rate5m` series appear
   and are flowing to long-term storage.

Regenerate and redeploy all `_shared/` artifacts whenever the curated app set
changes (new apps added, paths updated, or interfaces renumbered).

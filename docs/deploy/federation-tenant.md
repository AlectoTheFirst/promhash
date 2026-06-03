# Federation tenant deployment (C6) — DEPRECATED

> **Deprecated.** This model (per-app federation + per-app tenant Prometheus) has
> been superseded by the shared-evaluator projection model. See
> `docs/deploy/shared-evaluator.md` for the current deployment guide.
>
> This file is retained for historical reference only.

# Federation tenant deployment (C6)

promhash maps each curated application onto **one logical tenant**. A tenant is a
dedicated Prometheus instance whose job is to hold the curated, app-scoped slice of
metrics rather than the full firehose from the main Prometheus.

## What a tenant Prometheus runs

1. **Federation scrape config** — derived from the app's `federate.match` artifact
   (see `TenantScrapeConfig`). This is a single `/federate` scrape job that pulls
   only the metrics matching the app's `match[]` selector out of the main
   Prometheus. `honor_labels: true` preserves the upstream label set so the
   federated series keep their original identity.

2. **Recording rules** — the app's generated `rules.yaml` (direction-aware,
   per-hop recording rules from C5). These run inside the tenant Prometheus so the
   curated, pre-aggregated series are computed locally.

## Remote write to cloud LTS

The tenant `remote_write`s the curated series to the cloud long-term storage (LTS)
backend. Each tenant attaches an **`app`-scoped external label** so that LTS data is
partitioned per application:

```yaml
global:
  external_labels:
    app: payments
remote_write:
  - url: https://cloud-lts/api/v1/write
```

## GitOps delivery

Artifacts are delivered by the **existing GitOps pipeline**. promhash-enrich writes
each app's enrichment bundle to `gitops/enrichment/<app>/` (federation scrape config
+ `rules.yaml`). The pipeline applies `gitops/enrichment/<app>/` to the tenant
Prometheus for that app — no out-of-band deploy step is required.

## Per-app summary

| Concern             | Source                                   |
| ------------------- | ---------------------------------------- |
| Federation scrape   | `federate.match` → `TenantScrapeConfig`  |
| Recording rules     | generated `rules.yaml` (C5)              |
| Long-term storage   | `remote_write` with `app` external label |
| Delivery            | GitOps `gitops/enrichment/<app>/`        |

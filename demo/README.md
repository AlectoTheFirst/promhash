# promhash demo

A self-contained end-to-end stack: synthetic SNMP metrics → main Prometheus →
`remote_write` → promhash Prometheus (receiver) evaluating the generated
path-health rules, with the graph, API, and alert enrichment alongside. No
real devices, no Nautobot — device names come from `hostname` labels in the
`file_sd` target file, exactly like the production pattern.

## Run

```bash
docker compose up -d --build
docker compose logs -f init   # bootstrap: catalog -> loader -> enrich
```

The `init` job waits for the first scrape, syncs the catalog, validates and
loads `declared/*.yaml`, and writes the `_shared/` artifacts that the promhash
Prometheus and the mapping server consume.

## The synthetic topology

| Device | Interface | Role |
|---|---|---|
| rtr-edge-1 | Te0/0/1 | shared edge uplink (both apps) |
| rtr-core-1 | Te0/1/0 (`trunk-dc`) | payments transit; 1G trunk pinned at ~88% utilization |
| rtr-core-1 | Te0/1/1 | checkout transit; **flaps down 90s every 10 minutes** |
| rtr-dc-1 | Te1/0/0 | shared DC ingress |

`payments` (customer acme, tier-1) and `checkout` (customer globex, tier-2)
are the two curated apps.

## What to look at

| Where | What |
|---|---|
| http://localhost:9090 | main Prometheus: raw `if*` series with `hostname` labels; the `InterfaceDown` alert firing during flap windows |
| http://localhost:9091 | promhash Prometheus: query `app:path_util_max:ratio` (payments ≈ 0.88), `app:path_hops_down:count` (checkout 1 during flaps, explicit 0 otherwise), `app:path_alerts_firing:count`, and the `Promhash*` meta-alerts under Alerts |
| http://localhost:9093 | Alertmanager: during a flap, `InterfaceDown` arrives **enriched** — `promhash_max_criticality`, `promhash_app_count` labels and the `promhash_impact` annotation naming checkout/globex |
| http://localhost:8080 | promhash-api (token `demo-token`): `curl -H 'Authorization: Bearer demo-token' localhost:8080/apps/checkout/path` and `…/impact?device=rtr-core-1&ifName=Te0/1/1` |
| http://localhost:8428 | VictoriaMetrics (LTS stand-in): the remote-written `app:*` series with the `tenant=demo` label |
| http://localhost:7474 | Neo4j browser (neo4j / demopass): the graph itself |

Within ~10 minutes of startup you will see one full flap cycle: interface
down → `InterfaceDown` fires → proxy stamps blast radius → Alertmanager shows
the enriched alert → `app:path_hops_down:count{app="checkout"}` reads 1 →
recovery returns it to an explicit 0.

## Teardown

```bash
docker compose down -v
```

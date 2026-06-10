# promhash — Observability & Use-Case Review

**Lens:** senior observability / SRE / network-monitoring practitioner.
**Scope:** functionality, design, and operating model — *not* code. (Implementation bugs are
covered separately in `review.md`.)
**Date:** 2026-06-02
**Method:** read README + deploy docs, then an 8-lens specialist panel (cardinality, federation,
metric semantics, alerting/SLO, day-2 ops, data model/ecosystem, use-case realism, competitive
landscape) with two adversarial critics challenging every claim. Findings below are post-challenge.

---

## Verdict

promhash solves a **real, underserved problem**: a standard Prometheus + SNMP stack has no clean way
to answer "which business apps cross this interface" or "what's the blast radius of this link,"
because the relationship is genuinely many-to-many and you can't express it as a label without a
cardinality explosion. The instinct to keep that relationship out of the firehose and in a side
store with provenance and time is **correct**, and the zero-cardinality dashboard idea (app→interface
as a Grafana *variable*, never a *label*) is the cleverest piece of the design.

But the project **leads with its weakest argument and undersells its real moat**, ships the metric
layer through the **wrong data plane**, and is **trustworthy in a much narrower domain than it
advertises**. Concretely:

1. **The moat is temporal/impact/provenance + flow-blind-edge business context — not cardinality.**
   For one-app *path-health*, a bounded mapping series + `group_left` does the job inside the stack
   you already run. The graph earns its keep only where the answer is *not* a metric: point-in-time
   blast radius, the interface→many-apps direction (where PromQL is genuinely clumsy), and
   owner/customer/criticality context. Leading with cardinality invites the `group_left` rebuttal and
   makes evaluators walk before they see the moat.

2. **There is a missing middle tier.** The "never an app dimension" law correctly forbids tagging the
   firehose, but it also forbids the *cheap* answer to the common case: a static
   `promhash_interface_app{instance,ifIndex,app}=1` mapping series joined at query time. Ship that as a
   third tier (free graph lookup | bounded mapping series | full per-app projection).

3. **The per-app *tenant Prometheus* + `/federate` data plane is the wrong shape.** The curated
   slices are tiny by the design's own argument — they don't need a Prometheus each. Collapse to a
   single shared rule-evaluator (or an agent/Alloy/OTel collector scraping the exporter directly) +
   one `remote_write` with `app` as a write-relabel external label. "Tenant" should be a *logical*
   partition, not a process fleet.

4. **v1 is a chokepoint tool sold as a core tool.** Declared L3 paths are durable at policy-pinned
   chokepoints (DC leaf/spine uplinks, firewall/DMZ, dedicated/MPLS circuits, WAN edge, app
   first/last hop) and rot silently in the dynamic ECMP core the `rtr-core-1` examples foreground.
   The core is the *flow* story (Layer 2), not the *declared* story.

5. **Several links in the observability value chain are missing:** no SLIs beyond throughput, no
   drift detection, no surfaced staleness, no alert-enrichment wiring, and no self-observability of
   promhash itself.

**Bottom line:** adopt it as a *network blast-radius & business-context layer* scoped to stable
chokepoints, with the metric-projection tier demoted to an optional integration. Don't adopt it
expecting turnkey SLOs/alerting, and don't trust declared core paths during the incidents the tool
exists to illuminate — until Layer-2 flow lands. The decision hinges entirely on whether you need
the non-metric features (temporal forensics, business-dimension impact on shared infra); if you
don't, a mapping ConfigMap + recording rules gets you 70–80% at a fraction of the operational weight.

---

## What it gets right

- **It refuses the correct anti-pattern.** Stamping `app` on `ifHCInOctets`, or fanning out one series
  per `(interface, app)` across thousands of devices, is the #1 way infra→business mapping is
  attempted and the #1 way it melts a TSDB. promhash names the trap precisely and routes around it.
- **The free/curated tiering is the right cost discipline.** Most apps need only a relationship
  lookup (free); only apps with an owner, an SLO, and a pager need real `sum by(app)` series. Bounding
  derived-series cost to an intentional curated set is exactly how mature SRE orgs scope this.
- **Provenance + validity intervals are real observability hygiene.** "Who was affected last Tuesday
  at 14:05" against the topology *as it was then declared* is a capability neither Prometheus
  (no relationship history) nor flow tools (no clean as-of business mapping) offer cleanly. Closing
  validity intervals on re-declaration instead of hard-deleting is the correct temporal pattern.
- **Catalog harvested from live Prometheus is the right reconciliation anchor.** Resolving human
  interface refs against the labels that *actually exist* in Prometheus (not against what NetBox
  thinks exists) closes the classic "CMDB says it exists but it isn't scraped" gap. Treating `ifIndex`
  as a refreshable attribute keyed off canonical `ifName` is correct — `ifIndex` is volatile.
- **Git-as-source-of-truth fits the buyer.** PR-as-data-review, commit SHA as provenance, CI
  validation against the live catalog — this turns a data-quality problem into a code-review problem
  for an org already running GitOps, and is more honest than a CMDB whose app→CI links rot silently.
- **Honest restraint in the rules.** Refusing to sum octets across candidate ECMP paths (which would
  double-count without flow-derived weights) shows the design understands the difference between a
  model and a measurement.

---

## The core mispositioning: cardinality vs. the real value

The README treats "an `app` label on an infra metric" as a single forbidden act. It conflates two
very different designs:

- **(a) Tagging the live counter, or fanning out one series per `(interface, app)`** — scales with
  scrape rate × series, genuinely explosive, correctly forbidden.
- **(b) A separate static mapping series** `promhash_interface_app{instance,ifIndex,app}=1`, joined at
  query time with `group_left`. This never touches the infra metric, never scales with sample rate,
  and lives entirely in the Prometheus you already run.

The blanket law forbids (b) along with (a). (b) is the cheap solution to the **common** case and
should be a first-class output.

**The honest split (this is the key insight):**

| Question | Shape | Best tool |
|---|---|---|
| **Path-health** — "which interfaces does app X cross, are any saturated/down?" | one app → its interfaces (RHS unique once you pin `app="X"`) | **`group_left` mapping series wins.** The graph is *optional* here. One PromQL line, native alerting, no second datasource. |
| **Impact / blast-radius** — "this interface failed, which apps/customers are hit?" | one interface → many apps (join side non-unique) | **The graph wins.** `group_left` cannot express the many-on-the-join-side case cleanly; this is genuinely clumsy in pure PromQL, and the temporal/business dimensions live here. |

> *Correction applied:* the mapping-series-replaces-everything argument is ~80% right but overstated.
> A bounded mapping series is a cheaper **serving layer**, not a cheaper **system** — its source of
> truth still needs the catalog harvester + loader + enrich pipeline to stay accurate and
> `ifIndex`-current. You don't skip promhash to get the mapping series; you get it by adding one
> output to promhash. And it only collapses the **path-health** direction, not impact.

**Recommendation:** reframe the README to lead with point-in-time impact, provenance, and business
context (the things Prometheus structurally cannot do), demote cardinality to "why we don't tag the
firehose," and ship the bounded mapping series as the missing middle tier. This turns "never an app
dimension" from dogma into a sized, per-use-case cost decision and removes the strongest "this is
overkill" objection.

---

## The metric / projection layer (curated tier)

This is the weakest functional area. The graph is the differentiator; the emitted metric layer, as
designed, is in places a regression versus a competent hand-built SNMP dashboard.

- **`/federate` is the wrong transport, and tenant-per-app is the wrong topology.** Federation is
  sanctioned for pulling small *pre-aggregated* rollups, not as the data plane for fine-grained
  per-app SLO metrics. Re-pulling raw counters into a Prometheus-per-app adds: a fan-in of N pull
  clients against your most critical Prometheus, double-scrape staleness/jitter that degrades short
  `rate()` windows, and a linear operational tax (N TSDBs, WALs, remote_write queues, upgrade
  targets, failure domains).
  *Fix:* evaluate the (tiny) recording rules in one shared evaluator and `remote_write` once with
  `app` as a write-relabel external label; or, if isolation is truly wanted, scrape `snmp_exporter`
  directly with a stateless agent (Prometheus agent mode / Alloy / OTel collector). Same GitOps
  `rules.yaml`, far less surface.
  > *Correction applied:* "unrecoverable gaps that poison SLOs" is overstated for 64-bit `ifHC*`
  > counters — a missed federate scrape is recovered on the next one because `rate()` spans the gap,
  > and these counters effectively never wrap. The fair complaint is **staleness/jitter at incident
  > time** and the **lack of buffering**, plus the architecture depending on subtle, invisible-when-
  > wrong federation label semantics (`honor_labels` vs the rule matcher). And the "20–50 apps =
  > sprawl" threshold is asserted, not derived; curated sets are small by design, so the urgency is
  > lower than "50 Prometheis to patch" implies — the model is still wrong-shaped, just lower blast
  > radius.

- **The curated tier ships only half the headline signal.** The pitch is "are any saturated **or
  down**?" — but the only rules emitted are octet rates. `ifOperStatus` is federated yet never turned
  into a series; `ifHighSpeed`/`ifSpeed` (the capacity denominator) isn't collected at all; no
  error/discard counters. So **"saturated" has no denominator and "down" has no status series.** The
  tier pays its full operational cost to deliver a bits/sec chart.
  *Fix:* emit `app:if_oper_up:state` from `ifOperStatus`, an `app:if_capacity_bps` from
  `ifHighSpeed`, and document the path rollups (`max by(app)(util)`, `min by(app)(oper_up)`).

- **Per-hop series don't aggregate into a path-health number.** All hops share the record name; you
  can't `sum` them (an app's bytes counted once per hop) and `avg` isn't health. The README's own
  `sum by(app)` example double-counts an app's traffic once per declared hop — so adding a transit
  hop appears to *double* the app's traffic. The sound rollups (`max` utilization, count of down hops)
  require the missing capacity/status series.

- **The zero-cardinality dashboard PromQL is silently wrong on real networks.** `instance=~"$instance",
  ifIndex=~"$ifIndex"` is two independent regex sets — a **cross-product**. `ifIndex` integers are
  reused across devices, so a path spanning two routers matches phantom `(instanceA, ifIndexB)` pairs
  and pulls in unrelated interfaces, inflating traffic. This is the *free tier the long tail depends
  on.*
  *Fix:* a single composite key per hop (`iface="instance:ifIndex"`, or the stable `ifName` label) or
  an OR-of-exact-pairs; have the plugin frame carry `instance` + direction; ship a corrected reference
  dashboard.

- **`ifIndex` baked into rule exprs reintroduces the instability the model avoids.** The graph keys on
  `(device, ifName)` precisely because `ifIndex` renumbers — yet the emitted PromQL pins on `ifIndex`,
  so a linecard reconfig silently mis-selects until the next enrich run. Prefer the stable `ifName`
  label where `snmp_exporter` exposes it.

---

## Use-case realism: declared L3 paths vs. a dynamic network

This is the dominant risk to the whole value chain.

- **Hand-declaring hop-by-hop L3 paths fights the nature of L3 routing.** An app doesn't *have* a
  stable path across a routed core; the path is whatever OSPF/IS-IS/BGP best-path computes right now,
  and it changes on link failure, maintenance drains, metric/LP tuning, and ECMP rehashing. A
  declaration is a snapshot of *intent*. The moment a core link is drained, real traffic is elsewhere
  and the declaration is wrong — **and nothing in v1 detects it** (the catalog validates that an
  interface *exists*, not that traffic *traverses* it; the detector, flow, is Layer 2).
  → A blast-radius tool is least reliable during failures — exactly when it's needed.

- **How does a human even know the true path?** Authoring a correct hop list requires reading RIB/FIB
  across devices, BGP policy, ECMP buckets, VRF/MPLS. That knowledge lives with a few senior network
  engineers and goes stale. Realistically people declare the obvious first/last hops and hand-wave
  the core with one or two "representative" candidates. CI passes regardless, because it checks
  existence, not flow.

- **Impact is "potential," presented as fact.** The model concedes active/standby/weight are runtime
  facts (Layer 2). But `/impact` returns a flat list with a constant confidence and no
  provenance/redundancy/freshness field. On a healthy redundant/ECMP core — *the most common failure
  scenario* — a downed member implicates every app whose candidate set touches it, even when ECMP
  rehashed around it with zero user impact. Feed that to auto-paging and it's an **over-paging machine
  on the busiest links**, which trains on-call to ignore the enrichment → trust collapse → the whole
  Neo4j lifecycle cost becomes waste.
  > *Correction applied:* the design *does* concede potential-vs-active in prose (candidate paths,
  > "filled in later from flow"). The gap is **presentation** — the `/impact` JSON and the marketing
  > one-liner ("which applications and customers are affected") read as definite. *Cheap fix needing
  > no flow:* per affected app, return "sole declared path" vs "1 of N candidates" + mapping age, and
  > label the result "potential (declared)." Converts a flat over-report into something triageable.

- **The marketing examples point at the weakest domain.** Lead with chokepoints (durable, low-fan-out,
  policy-pinned); demote the `rtr-core-1` multipath example to "here's how candidate paths model the
  core, and why it's only potential until flow." Otherwise early adopters try the hardest case first
  and churn.

---

## Day-2 operability & lifecycle

A solid v1 data substrate, but not yet an *operable* system without significant operator-supplied glue.

- **Freshness asymmetry with no drift detector.** The catalog is continuously re-harvested; the paths
  that give the tool its value are PR-gated YAML re-validated only when someone touches the file. The
  two clocks drift, and a *partially* drifted path silently generates rules for the surviving hops and
  drops the vanished one. **Highest-leverage missing piece:** a scheduled job that replays every
  declared path against the current catalog (+ "does this interface carry any traffic / is
  `ifOperStatus` up") and emits `declared_paths_stale_total{reason=...}` plus a report. Reuses the
  existing resolver.

- **Staleness is stored but never surfaced.** `observedAt`/`valid_from` exist in the graph but
  `AppPath` and `/impact` return neither — a path declared 18 months ago is indistinguishable from one
  declared yesterday to every consumer. The design's own "report, don't silently trust" principle
  collapses if the consumer is never told the fact's age.

- **The observability tool can't be observed.** No `/metrics`, `/healthz`, `/readyz`; no
  `last_success_timestamp` on the scheduled catalog/enrich jobs; plain `log.Printf`. A scheduled sync
  that silently dies is the canonical Day-2 failure — everything keeps serving last-good data while
  drift accumulates. For a Prometheus-native audience this is table stakes and a credibility gap.

- **Catalog is upsert-only.** Decommissioned/renamed interfaces never expire; the catalog grows
  monotonically, "did you mean" suggestion lists fill with retired names, and resolution can bind to
  ghost interfaces. Needs a tombstone/close pass (with a grace window for transient scrape gaps),
  mirroring the validity-interval pattern already used for declarations.

- **No shipped scheduling/packaging, and a job-ordering footgun.** "Run on a schedule" ships nothing
  (no CronJob/timer/Workflow example), and the catalog-must-lead-enrich ordering dependency (enrich
  before catalog picks up a renumber → rules against a stale `ifIndex`) isn't called out.

- **No decommission flow.** Removing a YAML closes validity (good), but who tears down the tenant,
  reaps the `app`-labeled LTS series, and cleans `gitops/enrichment/<app>/`?

- **Neo4j is a 3-year commitment a metrics shop usually can't staff.** Backup/restore, HA, upgrades,
  Cypher on the on-call rotation. The graph is a materialization (git is SoT) so it's *rebuildable* —
  but only if seed/catalog/loader are idempotent against current external sources, which should be a
  tested DR drill. "Memgraph also works" is unverified portability.

---

## Alerting / SLO / on-call reality

- **promhash evaluates no alerts** — by design, and that's fine. The fair criticism is **incomplete
  SLIs**, not deceptive marketing: the README says curated apps get series "for SLOs, alerting" (build
  them in *your* stack), but the only emitted SLI is throughput, and the plugin returns label-only
  frames with no numeric/time field, so it's not a useful Grafana alert target.
  > *Correction applied:* don't frame this as "sells an alerting feature that doesn't exist." The
  > model is explicitly "promhash enriches; your existing stack alerts."

- **The strongest, most defensible use case is alert *enrichment* — and it isn't wired.** "Interface
  X down → page the owners of affected tier-1 apps/customers" is a real, underserved 3am need and is
  the use case *least* sensitive to path-accuracy fragility (a down interface's declared consumers are
  worth surfacing even if approximate). Ship a supported Alertmanager webhook receiver (or documented
  template) that calls `/impact` and injects app/owner/customer/criticality + a "potential, declared"
  caveat into annotations/routing. This should be the **day-1 deliverable**; today it's a "you could
  wire this up" aside.

- **Split-brain alerting domains.** Curated app series live in a tenant Prometheus; interface health
  lives in main Prometheus, with different scrape/eval timing. The "where does each alert live" map
  isn't documented.

---

## Competitive positioning — when to adopt, when not

| Alternative | How promhash compares |
|---|---|
| **Flow tools** (Kentik, Elastiflow, ntopng, pmacct, Scrutinizer) | Quantitatively attribute traffic to apps *where flow reaches* — the ground truth promhash defers to Layer 2. promhash's genuine niche is the **flow-blind edge** + a business overlay. In the flow-covered core, a flow tool answers impact-by-volume better and self-updates. **Complementary, not competing.** |
| **Prometheus-native** (mapping info-metric + `group_left`, recording rules, static ConfigMap) | The true build-vs-buy baseline and closest competitor. Delivers path-health dashboards with zero new infra. Can't natively do many-to-many impact at scale, point-in-time history, provenance, or identity resolution. **Below the threshold where those matter, the native pattern is the right size.** |
| **NetBox/Nautobot + gNMI/BMP/SuzieQ/Batfish** | Already own device/interface/cable/IP topology authoritatively and can *compute* the live L3 path from routing state — solving the "how does a human know the path" problem promhash punts to manual YAML. promhash should **derive** paths from these (roadmap "automated path discovery") rather than re-declare them. |
| **APM / service maps / eBPF** (Datadog, Dynatrace, Tempo service graph, Hubble, Kiali) | Auto-discover east-west *service* dependencies from traces/eBPF, but stop at the host/pod and never reach the physical interface. promhash is the inverse: weak/declared at the service layer, unique at the physical-interface layer. |
| **CMDB service mapping** (ServiceNow ITOM, Moogsoft/BigPanda) | Hold app→CI relationships and do event correlation/suppression promhash doesn't. promhash is lighter, git-reviewed, temporal, cardinality-safe — and could *feed* these a cleaner topology than a stale CMDB export. |
| **Do-nothing** (page the senior network engineer who knows it) | The realistic incumbent. promhash codifies tribal knowledge with review, history, and point-in-time queries — a genuine win **if the knowledge stays current**, which is exactly the drift risk above. |

**Reach for promhash when:** large Prometheus estate, real **flow-coverage gaps at the edge**, a
need for **point-in-time blast radius with business (owner/customer/criticality) context**, and a
willingness to scope declarations to stable chokepoints. **Don't, when:** you have flow everywhere
(run a flow tool), few apps or a static topology (a mapping metric), or your app paths live in service
mesh / cloud / SD-WAN (wrong layer — see gaps below).

---

## Concerns most reviews miss (raised by the adversarial pass)

- **Asymmetric / bidirectional routing breaks the "app's traffic" abstraction.** Return traffic
  routinely takes a different path than forward traffic. A per-hop `direction=egress/ingress`
  declaration captures at most one half of a conversation; the "app path" is really two paths, and a
  wrong `direction` silently records the reverse flow's counter on a plausible-looking panel.

- **SNMP scrape cadence sets a latency floor.** SNMP polling is commonly 30–120s (slow agents, big MIB
  walks). A `rate5m` over 60s samples already smears short saturation, so "is my path saturated *now*"
  has an inherent multi-minute detection latency *before any federation*. Tempers expectations vs.
  flow/streaming-telemetry tools that see microbursts.

- **No RBAC on customer-attributed data.** The graph holds customer names, owners, and tier-1
  criticality; `/impact` returns customer per app to *any* caller. For the chargeback/per-customer
  roadmap (and any MSP/regulated context) "front it with a proxy" is not an authz model — there's no
  notion of who may see which customer's blast radius, and no query audit.

- **The whole resolver depends on `snmp_exporter` label hygiene.** It assumes
  `ifName`/`ifDescr`/`ifAlias`/`ifIndex` are present on `ifHC*Octets`. Many shops scrape `ifIndex`
  only (to control cardinality) or have empty/free-text `ifAlias`; then human-ref matching degrades to
  `ifIndex`-only — reintroducing the volatile key the model exists to avoid. This is an unstated
  precondition for the tool working at all.

- **Graph and LTS can disagree about history.** Once curated series are `remote_write`n to LTS with an
  `app` external label, those samples are immutable. Retract or correct a path later and the graph's
  point-in-time answer for last Tuesday disagrees with the LTS series for last Tuesday. The
  temporal-honesty guarantee stops at the graph boundary; the projected series have no
  retraction/backfill story.

- **Day-1 / bootstrap ROI is back-loaded.** The "who was affected last Tuesday" moat is worthless in
  week one (empty history) and compounds with tenure. Meanwhile authoring the *first* path for an app
  nobody has documented is the hardest moment, with no assisted discovery (seed candidate hops from
  traceroute, or CMDB app→IP + Nautobot topology, or a one-off flow sample). Lead adoption with the
  enrichment recipe (fast value) and let the temporal asset accrue.

- **Multi-region / multi-Prometheus is unmodeled.** `instance`/`ifIndex` are unique only within a
  scrape domain; regional Prometheis can scrape the same device with different `instance` values.
  promhash has no region/scrape-domain in its identity model — ambiguous exactly for the large estate
  that would justify its weight.

- **`phash` is deterministic name-hashing, not entity reconciliation.** The README oversells it
  ("seen as an IP, FQDN, sys_id, serial... collapses to one node"); in practice cross-source joins
  succeed only when CMDB and YAML authors type byte-identical (normalized) app/service names. The
  practical join surface is narrow (and interfaces are keyed observationally, sidestepping the hard
  part), so this is a **README wording fix**, not a load-bearing runtime corruption — but the durable
  `sys_id` is captured and never used as the join key, which it should be.

---

## Gaps in the observability value chain

- No bounded mapping-series output (the missing middle tier).
- No quantitative attribution yet — curated per-hop rates are **whole-link totals filtered to an
  app's path, not the app's share**. Until Layer 2, a panel reading "payments = 10 Gbps" on a shared
  10 Gbps link is misleading; needs a prominent caveat / a distinct metric name.
- No drift/reconciliation loop against observed forwarding (flow, BGP-LS, gNMI, LLDP, traceroute).
- No saturation primitive (capacity), no `ifOperStatus`/error/discard series, no path-level rollup.
- No alert-enrichment artifact despite enrichment being the strongest use case.
- No self-observability; no surfaced staleness/confidence at the API boundary.
- Device is not a first-class node → no device-/site-level impact (most real questions are "the FRA
  PoP router is down," not per-interface).
- `DEPENDS_ON` (the east-west service graph — the query a graph is *best* at) is write-only; not
  exposed. Exposing transitive dependency impact would most justify the graph DB.
- Interface-on-device (SNMP) centric → silent on service mesh / cloud VPC / SD-WAN overlay / LB
  segments where much modern app traffic lives.

---

## Prioritized recommendations

| # | Recommendation | Effort | Why |
|---|---|---|---|
| 1 | **Reframe the README**: lead with temporal/impact/provenance + flow-blind-edge business context; demote cardinality to "why we don't tag the firehose"; state the honest path-health-vs-impact split. | small | Points evaluators at the moat, not the rebuttable argument. |
| 2 | **Mark impact "potential" and carry redundancy + freshness** (`sole path` vs `1 of N`, mapping age, provenance/confidence) in `/impact` and the plugin. | medium | Directly defuses the over-paging / trust-collapse risk; needs **no flow**. |
| 3 | **Ship a supported Alertmanager enrichment recipe** (webhook → `/impact` → annotations/routing). | medium | Operationalizes the strongest, accuracy-tolerant use case as the day-1 win. |
| 4 | **Add the bounded mapping series as a third tier** + document the `group_left` join. | medium | Makes the common path-health case a one-liner in the existing stack, natively alertable. |
| 5 | **Collapse federation/tenant → single shared rule-eval + one `remote_write`** (or direct-scrape agent); make "tenant" a logical external-label partition. | medium | Removes N-Prometheis sprawl, the fan-in, and the staleness/jitter hop; same `rules.yaml`. |
| 6 | **Emit `ifOperStatus` + capacity (`ifHighSpeed`) series and path rollups.** | medium | Delivers the "saturated or down" half of the headline that's currently missing. |
| 7 | **Add a scheduled declared-path drift detector** (replay vs catalog + "carries traffic?"); surface `observedAt`/`valid_from` everywhere a fact is consumed; emit a staleness metric. | medium | The difference between a data store and an operable Day-2 system. |
| 8 | **Instrument every binary** (`/metrics`, `/healthz`, `last_success_timestamp`); give the catalog a tombstone lifecycle. | medium | A Prometheus-native audience won't trust a tool it can't scrape or alert on. |
| 9 | **Reposition examples around chokepoints**; state the curated scope honestly ("a few dozen golden paths," not 500) and where *not* to use it. | small | Aligns the pitch with where the tool is trustworthy; prevents churn on the hardest case. |
| 10 | **Fix the zero-cardinality dashboard pattern** (composite key / exact pairs; plugin frame carries `instance` + direction; ship a reference dashboard). | medium | The long-tail "free" value doesn't materialize as documented until this is hardened. |
| 11 | **Add an authz/RBAC model** before exposing customer-attributed impact beyond one SRE team. | medium | Governance prerequisite for the per-customer/chargeback roadmap. |
| 12 | **Consume Nautobot topology + computed paths (Batfish/BGP-LS) and flow (Layer 2) as `provenance=topology/flow` overlays.** | large | Fixes drift at the root by giving the declared layer something observed to reconcile against; turns the graph into a join over SoT rather than a parallel hand-maintained one. |

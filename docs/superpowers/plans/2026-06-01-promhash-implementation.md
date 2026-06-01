# promhash Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build promhash — a Neo4j application-path graph plus the enrichment, API, normalization, and Grafana-plugin tooling that maps classic network metrics to business applications without ever adding an `app` label to the high-cardinality infra firehose.

**Architecture:** A property graph (Neo4j) holds CSDM business entities, reified directional Connections, and ordered candidate Paths over interface nodes. Declared YAML-in-git is validated against a live interface catalog (harvested from Prometheus) and loaded into the graph; an enrichment generator emits per-curated-app federation + recording-rule GitOps artifacts; a REST API + Go Grafana datasource plugin serve path-health and impact for every app at zero added cardinality.

**Tech Stack:** Go 1.23 · Neo4j 5 (`neo4j-go-driver/v5`) · `testcontainers-go` · `prometheus/client_golang` · `gopkg.in/yaml.v3` · stdlib `net/http` (Go 1.22 routing) · `grafana-plugin-sdk-go` + React/TypeScript (plugin frontend).

**Spec:** `docs/superpowers/specs/2026-05-30-promhash-network-to-app-mapping-design.md` (components C1–C9).

---

## Shared Contracts (referenced by every task — do not drift)

**Module:** `github.com/starkweb/promhash`

**Package layout:**
```
cmd/promhash-api/main.go        cmd/promhash-loader/main.go     cmd/promhash-catalog/main.go
cmd/promhash-enrich/main.go     cmd/promhash-seed/main.go
internal/phash/                 # C2 identity hashing
internal/graph/                 # C1 Neo4j model + repo
internal/catalog/               # C9 canon + resolver + sync
internal/declare/               # C4 YAML types + validate + load
internal/enrich/                # C5 traverse + federation + rules
internal/promclient/            # Prometheus query client (catalog harvest)
internal/nautobot/              # Nautobot client (device↔instance)
internal/servicenow/            # ServiceNow client (seed)
internal/api/                   # C7 HTTP server + handlers
plugin/promhash-datasource/     # C8 Grafana plugin (Go backend + React)
internal/testutil/              # neo4j testcontainer helper
```

**Core types (defined in Task 1.1 / 2.1, used everywhere):**
```go
// internal/phash
type Kind string
const ( KindDevice Kind="device"; KindIface Kind="interface"; KindIP Kind="ip";
        KindEndpoint Kind="endpoint"; KindApp Kind="application"; KindAppSvc Kind="appservice";
        KindBizSvc Kind="businessservice"; KindCustomer Kind="customer"; KindSegment Kind="segment" )
func Hash(k Kind, parts ...string) string   // "kind:" + 16-hex of sha256 over normalized parts

// internal/graph — node structs
type Iface struct {
    PHash, Device, IfName, MetricIfName, IfDescr, IfAlias, Instance, Vendor string
    IfIndex int
    ObservedAt time.Time
}
type Hop struct { Device, IfName, MetricIfName, Instance, Direction string; IfIndex, Seq int; Provenance string; Confidence float64 }
type ImpactRow struct { App, Service, Customer, Owner, Criticality string }
```

**Conventions:**
- Integration tests (need Neo4j/HTTP mocks-as-containers) carry `//go:build integration` and run via `make test-int`. Unit tests run via `make test`.
- Every commit message: `feat:`/`test:`/`chore:` prefix, end with `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`.
- `provenance` is one of `declared|flow|topology|observed`. v1 declared loader writes `declared`; catalog writes `observed`.

---

## Phase 0 — Scaffold

### Task 0.1: Go module + Makefile + layout

**Files:**
- Create: `go.mod`, `Makefile`, `.gitignore`, `internal/testutil/doc.go`

- [ ] **Step 1: Initialize module and tidy**

Run:
```bash
cd /Users/chris/code/ai-code/promhash
go mod init github.com/starkweb/promhash
printf '/bin/\n*.out\nnode_modules/\nplugin/promhash-datasource/dist/\n' > .gitignore
```

- [ ] **Step 2: Write the Makefile**

```makefile
# Makefile
.PHONY: test test-int build lint
test:        ; go test ./...
test-int:    ; go test -tags=integration ./...
build:       ; go build ./...
lint:        ; go vet ./...
```

- [ ] **Step 3: Add a package doc anchor so the module builds**

```go
// internal/testutil/doc.go
// Package testutil provides shared test harnesses (Neo4j testcontainer, HTTP mocks).
package testutil
```

- [ ] **Step 4: Verify it builds**

Run: `go build ./... && go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add go.mod .gitignore Makefile internal/testutil/doc.go
git commit -m "chore: scaffold go module and make targets

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 0.2: Neo4j testcontainer helper

**Files:**
- Create: `internal/testutil/neo4j.go`, `internal/testutil/neo4j_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package testutil

import ( "context"; "testing" )

func TestNeo4jContainerConnects(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := Neo4j(t, ctx)
    defer cleanup()
    if err := drv.VerifyConnectivity(ctx); err != nil {
        t.Fatalf("connectivity: %v", err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/testutil/ -run TestNeo4jContainerConnects -v`
Expected: FAIL — `undefined: Neo4j`.

- [ ] **Step 3: Implement the helper**

```go
//go:build integration
package testutil

import (
    "context"; "testing"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    tcneo4j "github.com/testcontainers/testcontainers-go/modules/neo4j"
)

// Neo4j starts a throwaway Neo4j 5 container and returns a connected driver.
func Neo4j(t *testing.T, ctx context.Context) (neo4j.DriverWithContext, func()) {
    t.Helper()
    c, err := tcneo4j.Run(ctx, "neo4j:5.23", tcneo4j.WithAdminPassword("testpass"))
    if err != nil { t.Fatalf("start neo4j: %v", err) }
    uri, err := c.BoltURL(ctx)
    if err != nil { t.Fatalf("bolt url: %v", err) }
    drv, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth("neo4j", "testpass", ""))
    if err != nil { t.Fatalf("driver: %v", err) }
    return drv, func() { _ = drv.Close(ctx); _ = c.Terminate(ctx) }
}
```

Run: `go get github.com/neo4j/neo4j-go-driver/v5 github.com/testcontainers/testcontainers-go/modules/neo4j`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/testutil/ -run TestNeo4jContainerConnects -v`
Expected: PASS (requires Docker running).

- [ ] **Step 5: Commit**

```bash
git add internal/testutil/neo4j.go internal/testutil/neo4j_test.go go.mod go.sum
git commit -m "test: add neo4j testcontainer helper

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 1 — C2 Identity (`internal/phash`)

### Task 1.1: Deterministic `phash.Hash`

**Files:**
- Create: `internal/phash/phash.go`, `internal/phash/phash_test.go`

- [ ] **Step 1: Write the failing test**

```go
package phash

import "testing"

func TestHashCanonicalAndStable(t *testing.T) {
    a := Hash(KindIface, "RTR-Core-1", " Te0/1/2 ")
    b := Hash(KindIface, "rtr-core-1", "te0/1/2")
    if a != b { t.Fatalf("canonicalization failed: %q != %q", a, b) }
    if got := Hash(KindIface, "rtr-core-1", "te0/1/2"); got != a {
        t.Fatalf("not deterministic: %q != %q", got, a)
    }
    if a[:10] != "interface:" { t.Fatalf("missing kind prefix: %q", a) }
}

func TestHashDistinctAcrossKindAndKeys(t *testing.T) {
    if Hash(KindDevice, "x") == Hash(KindIface, "x") { t.Fatal("kind not in hash") }
    if Hash(KindIface, "a") == Hash(KindIface, "b") { t.Fatal("keys not in hash") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/phash/ -v`
Expected: FAIL — `undefined: Hash`.

- [ ] **Step 3: Implement**

```go
package phash

import ( "crypto/sha256"; "encoding/hex"; "strings" )

type Kind string

const (
    KindDevice   Kind = "device"
    KindIface    Kind = "interface"
    KindIP       Kind = "ip"
    KindEndpoint Kind = "endpoint"
    KindApp      Kind = "application"
    KindAppSvc   Kind = "appservice"
    KindBizSvc   Kind = "businessservice"
    KindCustomer Kind = "customer"
    KindSegment  Kind = "segment"
)

// Hash returns a stable id "kind:<16 hex>" over case/space-normalized parts.
func Hash(k Kind, parts ...string) string {
    norm := make([]string, len(parts))
    for i, p := range parts { norm[i] = strings.ToLower(strings.TrimSpace(p)) }
    h := sha256.Sum256([]byte(string(k) + "\x1f" + strings.Join(norm, "\x1f")))
    return string(k) + ":" + hex.EncodeToString(h[:])[:16]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/phash/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/phash/
git commit -m "feat: deterministic identity hashing (C2)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 2 — C1 Graph (`internal/graph`)

### Task 2.1: Model structs + schema constants

**Files:**
- Create: `internal/graph/model.go`, `internal/graph/model_test.go`

- [ ] **Step 1: Write the failing test**

```go
package graph

import "testing"

func TestIfaceZeroValueFields(t *testing.T) {
    var i Iface
    if i.IfIndex != 0 || i.PHash != "" { t.Fatal("unexpected zero values") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/graph/ -v`
Expected: FAIL — `undefined: Iface`.

- [ ] **Step 3: Implement model**

```go
package graph

import "time"

const (
    LabelInterface = "Interface"
    LabelDevice    = "Device"
    RelHop         = "HOP"
    RelTakes       = "TAKES"
    RelDependsOn   = "DEPENDS_ON"
)

type Iface struct {
    PHash, Device, IfName, MetricIfName, IfDescr, IfAlias, Instance, Vendor string
    IfIndex    int
    ObservedAt time.Time
}

type Hop struct {
    Device       string  `json:"device"`
    IfName       string  `json:"ifName"`
    MetricIfName string  `json:"metricIfName"`
    Instance     string  `json:"instance"`
    Direction    string  `json:"direction"`
    IfIndex      int     `json:"ifIndex"`
    Seq          int     `json:"seq"`
    Provenance   string  `json:"provenance"`
    Confidence   float64 `json:"confidence"`
}

type ImpactRow struct {
    App         string `json:"app"`
    Service     string `json:"service"`
    Customer    string `json:"customer"`
    Owner       string `json:"owner"`
    Criticality string `json:"criticality"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/graph/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/model.go internal/graph/model_test.go
git commit -m "feat: graph model structs and label constants (C1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 2.2: Repo + `EnsureConstraints`

**Files:**
- Create: `internal/graph/repo.go`, `internal/graph/repo_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package graph

import ( "context"; "testing"; "github.com/starkweb/promhash/internal/testutil" )

func TestEnsureConstraintsIdempotent(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j")
    if err := r.EnsureConstraints(ctx); err != nil { t.Fatal(err) }
    if err := r.EnsureConstraints(ctx); err != nil { t.Fatalf("second call: %v", err) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestEnsureConstraints -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement repo + constraints**

```go
package graph

import (
    "context"; "fmt"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type Repo struct { drv neo4j.DriverWithContext; db string }

func New(drv neo4j.DriverWithContext, db string) *Repo { return &Repo{drv: drv, db: db} }

func (r *Repo) write(ctx context.Context, cy string, params map[string]any) error {
    _, err := neo4j.ExecuteQuery(ctx, r.drv, cy, params,
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    return err
}

func (r *Repo) EnsureConstraints(ctx context.Context) error {
    for _, label := range []string{LabelInterface, LabelDevice, "Application", "ApplicationService",
        "BusinessService", "Customer", "Endpoint", "IP", "Segment"} {
        cy := fmt.Sprintf(
            "CREATE CONSTRAINT phash_%s IF NOT EXISTS FOR (n:%s) REQUIRE n.phash IS UNIQUE", label, label)
        if err := r.write(ctx, cy, nil); err != nil { return err }
    }
    return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestEnsureConstraints -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/graph/repo_test.go
git commit -m "feat: graph repo with idempotent phash constraints (C1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 2.3: Upsert + read interfaces

**Files:**
- Modify: `internal/graph/repo.go`
- Modify: `internal/graph/repo_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package graph

import ( "context"; "testing"; "time"; "github.com/starkweb/promhash/internal/testutil" )

func TestUpsertAndGetInterface(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    in := Iface{PHash: "interface:abc", Device: "rtr-core-1", IfName: "te0/1/2",
        MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc",
        Instance: "10.0.0.1", Vendor: "cisco-iosxr", IfIndex: 42, ObservedAt: time.Unix(1700000000, 0).UTC()}
    if err := r.UpsertInterface(ctx, in); err != nil { t.Fatal(err) }
    in.IfIndex = 43 // re-upsert updates the volatile attr, same node
    if err := r.UpsertInterface(ctx, in); err != nil { t.Fatal(err) }
    got, err := r.GetInterfaceByPHash(ctx, "interface:abc")
    if err != nil { t.Fatal(err) }
    if got.IfIndex != 43 || got.IfAlias != "uplink-ledger-dc" { t.Fatalf("got %+v", got) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertAndGetInterface -v`
Expected: FAIL — `undefined: (*Repo).UpsertInterface`.

- [ ] **Step 3: Implement upsert + get**

```go
// append to internal/graph/repo.go
func (r *Repo) UpsertInterface(ctx context.Context, i Iface) error {
    return r.write(ctx,
        `MERGE (n:Interface {phash:$phash})
         SET n.device=$device, n.ifName=$ifName, n.metricIfName=$metricIfName,
             n.ifDescr=$ifDescr, n.ifAlias=$ifAlias, n.instance=$instance,
             n.vendor=$vendor, n.ifIndex=$ifIndex, n.observedAt=$observedAt, n.provenance='observed'`,
        map[string]any{"phash": i.PHash, "device": i.Device, "ifName": i.IfName,
            "metricIfName": i.MetricIfName, "ifDescr": i.IfDescr, "ifAlias": i.IfAlias,
            "instance": i.Instance, "vendor": i.Vendor, "ifIndex": i.IfIndex,
            "observedAt": i.ObservedAt.Unix()})
}

func (r *Repo) GetInterfaceByPHash(ctx context.Context, phash string) (Iface, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv,
        `MATCH (n:Interface {phash:$phash}) RETURN n`, map[string]any{"phash": phash},
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil { return Iface{}, err }
    if len(res.Records) == 0 { return Iface{}, ErrNotFound }
    n, _ := res.Records[0].Get("n")
    props := n.(neo4j.Node).Props
    return ifaceFromProps(props), nil
}

var ErrNotFound = fmt.Errorf("graph: node not found")

func ifaceFromProps(p map[string]any) Iface {
    gs := func(k string) string { if v, ok := p[k].(string); ok { return v }; return "" }
    gi := func(k string) int { if v, ok := p[k].(int64); ok { return int(v) }; return 0 }
    return Iface{PHash: gs("phash"), Device: gs("device"), IfName: gs("ifName"),
        MetricIfName: gs("metricIfName"), IfDescr: gs("ifDescr"), IfAlias: gs("ifAlias"),
        Instance: gs("instance"), Vendor: gs("vendor"), IfIndex: gi("ifIndex")}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertAndGetInterface -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/graph/repo_test.go
git commit -m "feat: upsert and read Interface nodes (C1)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 3 — C9 Catalog + Resolver (`internal/catalog`, `internal/promclient`, `internal/nautobot`)

### Task 3.1: Vendor canonicalization

**Files:**
- Create: `internal/catalog/canon.go`, `internal/catalog/canon_test.go`

- [ ] **Step 1: Write the failing test**

```go
package catalog

import "testing"

func TestCanonicalIfName(t *testing.T) {
    cases := map[[2]string]string{
        {"cisco", "Gi0/3"}:        "gigabitethernet0/3",
        {"cisco", "GigabitEthernet0/3"}: "gigabitethernet0/3",
        {"cisco", "Te0/1/2"}:      "tengige0/1/2",
        {"juniper", "ge-0/0/3"}:   "ge-0/0/3",
        {"arista", "Eth3"}:        "ethernet3",
        {"", "  Te0/1/2 "}:        "tengige0/1/2",
    }
    for in, want := range cases {
        if got := CanonicalIfName(in[0], in[1]); got != want {
            t.Errorf("CanonicalIfName(%q,%q)=%q want %q", in[0], in[1], got, want)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/catalog/ -run TestCanonicalIfName -v`
Expected: FAIL — `undefined: CanonicalIfName`.

- [ ] **Step 3: Implement canonicalization**

```go
package catalog

import ( "regexp"; "strings" )

// abbrev maps short vendor forms to long; applied longest-prefix-first.
var abbrev = []struct{ short, long string }{
    {"tengige", "tengige"}, {"te", "tengige"},
    {"gigabitethernet", "gigabitethernet"}, {"gi", "gigabitethernet"},
    {"ethernet", "ethernet"}, {"eth", "ethernet"},
    {"fastethernet", "fastethernet"}, {"fa", "fastethernet"},
    {"hundredgige", "hundredgige"}, {"hu", "hundredgige"},
}

var prefixRe = regexp.MustCompile(`^([a-z]+)(.*)$`)

// CanonicalIfName lowercases, trims, and expands vendor abbreviations on the
// leading alpha token. Juniper-style names (with '-') are left as-is after norm.
func CanonicalIfName(vendor, raw string) string {
    s := strings.ToLower(strings.TrimSpace(raw))
    if strings.Contains(s, "-") { return s } // juniper ge-0/0/3, xe-0/1/2
    m := prefixRe.FindStringSubmatch(s)
    if m == nil { return s }
    head, tail := m[1], m[2]
    best := head
    for _, a := range abbrev {
        if head == a.short { best = a.long; break }
    }
    return best + tail
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/catalog/ -run TestCanonicalIfName -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/canon.go internal/catalog/canon_test.go
git commit -m "feat: vendor interface-name canonicalization (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 3.2: Resolver — match human ref to one catalog interface

**Files:**
- Create: `internal/catalog/resolver.go`, `internal/catalog/resolver_test.go`

- [ ] **Step 1: Write the failing test**

```go
package catalog

import ( "testing"; "github.com/starkweb/promhash/internal/graph" )

func cat() []graph.Iface {
    return []graph.Iface{
        {PHash: "interface:1", Device: "rtr-core-1", IfName: "tengige0/1/2",
            MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
        {PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/1/3",
            MetricIfName: "Te0/1/3", IfDescr: "TenGigE0/1/3", IfAlias: "uplink-auth-dc", Vendor: "cisco"},
    }
}

func TestResolveByName(t *testing.T) {
    r := NewResolver(cat())
    got, err := r.Resolve("rtr-core-1", "Te0/1/2")
    if err != nil { t.Fatal(err) }
    if got.PHash != "interface:1" { t.Fatalf("got %s", got.PHash) }
}

func TestResolveByAlias(t *testing.T) {
    r := NewResolver(cat())
    got, err := r.Resolve("rtr-core-1", "uplink-ledger-dc")
    if err != nil { t.Fatal(err) }
    if got.PHash != "interface:1" { t.Fatalf("got %s", got.PHash) }
}

func TestResolveNoMatchFailsLoud(t *testing.T) {
    r := NewResolver(cat())
    _, err := r.Resolve("rtr-core-1", "Te9/9/9")
    var nm *NoMatchError
    if !errorsAs(err, &nm) { t.Fatalf("want NoMatchError, got %v", err) }
    if len(nm.Suggestions) == 0 { t.Fatal("expected suggestions") }
}
```

(Helper `errorsAs` is a thin wrapper over `errors.As` declared in the test file.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/catalog/ -run TestResolve -v`
Expected: FAIL — `undefined: NewResolver`.

- [ ] **Step 3: Implement resolver**

```go
package catalog

import ( "fmt"; "sort"; "strings"; "github.com/starkweb/promhash/internal/graph" )

type NoMatchError struct{ Device, Ref string; Suggestions []string }
func (e *NoMatchError) Error() string {
    return fmt.Sprintf("no interface on %q matches %q; did you mean: %s",
        e.Device, e.Ref, strings.Join(e.Suggestions, ", "))
}
type AmbiguousError struct{ Device, Ref string; Matches []string }
func (e *AmbiguousError) Error() string {
    return fmt.Sprintf("ref %q on %q is ambiguous: %s", e.Ref, e.Device, strings.Join(e.Matches, ", "))
}

type Resolver struct{ byDevice map[string][]graph.Iface }

func NewResolver(ifaces []graph.Iface) *Resolver {
    m := map[string][]graph.Iface{}
    for _, i := range ifaces { m[i.Device] = append(m[i.Device], i) }
    return &Resolver{byDevice: m}
}

// Resolve maps (device, human ref) to exactly one catalog interface, matching
// against canonical ifName, ifDescr, or ifAlias. Zero/many matches fail loud.
func (r *Resolver) Resolve(device, ref string) (graph.Iface, error) {
    list := r.byDevice[device]
    want := CanonicalIfName("", ref)
    refLower := strings.ToLower(strings.TrimSpace(ref))
    var hits []graph.Iface
    for _, i := range list {
        switch {
        case i.IfName == want,
            CanonicalIfName(i.Vendor, i.IfDescr) == want,
            strings.ToLower(i.IfAlias) == refLower,
            strings.ToLower(i.MetricIfName) == refLower:
            hits = append(hits, i)
        }
    }
    switch len(hits) {
    case 1:
        return hits[0], nil
    case 0:
        return graph.Iface{}, &NoMatchError{Device: device, Ref: ref, Suggestions: suggest(list)}
    default:
        names := make([]string, len(hits))
        for i, h := range hits { names[i] = h.MetricIfName }
        return graph.Iface{}, &AmbiguousError{Device: device, Ref: ref, Matches: names}
    }
}

func suggest(list []graph.Iface) []string {
    out := make([]string, 0, len(list))
    for _, i := range list { out = append(out, i.MetricIfName) }
    sort.Strings(out)
    if len(out) > 8 { out = out[:8] }
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/catalog/ -run TestResolve -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/resolver.go internal/catalog/resolver_test.go
git commit -m "feat: interface resolver with fail-loud matching (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 3.3: Prometheus harvest client

**Files:**
- Create: `internal/promclient/prom.go`, `internal/promclient/prom_test.go`

- [ ] **Step 1: Write the failing test**

```go
package promclient

import ( "context"; "net/http"; "net/http/httptest"; "testing" )

func TestHarvestInterfaces(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
          {"metric":{"instance":"10.0.0.1","ifIndex":"42","ifName":"Te0/1/2","ifDescr":"TenGigE0/1/2","ifAlias":"uplink-ledger-dc"},"value":[0,"1"]}
        ]}}`))
    }))
    defer srv.Close()
    c, _ := New(srv.URL)
    rows, err := c.HarvestInterfaces(context.Background())
    if err != nil { t.Fatal(err) }
    if len(rows) != 1 || rows[0].IfIndex != 42 || rows[0].Instance != "10.0.0.1" {
        t.Fatalf("got %+v", rows)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/promclient/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement harvest client**

```go
package promclient

import (
    "context"; "strconv"; "time"
    promapi "github.com/prometheus/client_golang/api"
    v1 "github.com/prometheus/client_golang/api/prometheus/v1"
    "github.com/prometheus/common/model"
)

type IfaceRow struct{ Instance, IfName, IfDescr, IfAlias string; IfIndex int }

type Client struct{ api v1.API }

func New(addr string) (*Client, error) {
    c, err := promapi.NewClient(promapi.Config{Address: addr})
    if err != nil { return nil, err }
    return &Client{api: v1.NewAPI(c)}, nil
}

const harvestQuery = `group by (instance, ifIndex, ifName, ifDescr, ifAlias) (ifHCInOctets)`

func (c *Client) HarvestInterfaces(ctx context.Context) ([]IfaceRow, error) {
    val, _, err := c.api.Query(ctx, harvestQuery, time.Time{})
    if err != nil { return nil, err }
    vec, ok := val.(model.Vector)
    if !ok { return nil, nil }
    out := make([]IfaceRow, 0, len(vec))
    for _, s := range vec {
        idx, _ := strconv.Atoi(string(s.Metric["ifIndex"]))
        out = append(out, IfaceRow{
            Instance: string(s.Metric["instance"]), IfName: string(s.Metric["ifName"]),
            IfDescr: string(s.Metric["ifDescr"]), IfAlias: string(s.Metric["ifAlias"]), IfIndex: idx})
    }
    return out, nil
}
```

Run: `go get github.com/prometheus/client_golang github.com/prometheus/common`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/promclient/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/promclient/ go.mod go.sum
git commit -m "feat: prometheus interface harvest client (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 3.4: Nautobot device↔instance client

**Files:**
- Create: `internal/nautobot/nautobot.go`, `internal/nautobot/nautobot_test.go`

- [ ] **Step 1: Write the failing test**

```go
package nautobot

import ( "context"; "net/http"; "net/http/httptest"; "testing" )

func TestDeviceInstanceMap(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Write([]byte(`{"results":[{"name":"rtr-core-1","primary_ip":{"address":"10.0.0.1/32"}}]}`))
    }))
    defer srv.Close()
    c := New(srv.URL, "token")
    m, err := c.DeviceInstanceMap(context.Background())
    if err != nil { t.Fatal(err) }
    if m["rtr-core-1"] != "10.0.0.1" { t.Fatalf("got %v", m) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/nautobot/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement client**

```go
package nautobot

import (
    "context"; "encoding/json"; "net/http"; "strings"; "time"
)

type Client struct{ base, token string; hc *http.Client }

func New(base, token string) *Client {
    return &Client{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{Timeout: 30 * time.Second}}
}

type deviceList struct {
    Results []struct {
        Name      string `json:"name"`
        PrimaryIP *struct{ Address string `json:"address"` } `json:"primary_ip"`
    } `json:"results"`
}

// DeviceInstanceMap returns device name -> management IP (the Prometheus `instance` host).
func (c *Client) DeviceInstanceMap(ctx context.Context) (map[string]string, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/dcim/devices/?limit=0", nil)
    if c.token != "" { req.Header.Set("Authorization", "Token "+c.token) }
    resp, err := c.hc.Do(req)
    if err != nil { return nil, err }
    defer resp.Body.Close()
    var dl deviceList
    if err := json.NewDecoder(resp.Body).Decode(&dl); err != nil { return nil, err }
    out := map[string]string{}
    for _, d := range dl.Results {
        if d.PrimaryIP != nil {
            out[d.Name] = strings.SplitN(d.PrimaryIP.Address, "/", 2)[0]
        }
    }
    return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/nautobot/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/nautobot/
git commit -m "feat: nautobot device-to-instance client (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 3.5: Catalog sync (harvest → graph)

**Files:**
- Create: `internal/catalog/sync.go`, `internal/catalog/sync_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package catalog

import ( "context"; "testing"
    "github.com/starkweb/promhash/internal/graph"; "github.com/starkweb/promhash/internal/promclient"
    "github.com/starkweb/promhash/internal/testutil" )

func TestSyncUpsertsInterfaces(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := graph.New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    rows := []promclient.IfaceRow{{Instance: "10.0.0.1", IfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", IfIndex: 42}}
    devByInstance := map[string]string{"10.0.0.1": "rtr-core-1"}
    if err := Sync(ctx, r, rows, devByInstance, "cisco"); err != nil { t.Fatal(err) }
    got, err := r.GetInterfaceByPHash(ctx, ifacePHash("rtr-core-1", "tengige0/1/2"))
    if err != nil { t.Fatal(err) }
    if got.MetricIfName != "Te0/1/2" || got.Device != "rtr-core-1" { t.Fatalf("got %+v", got) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/catalog/ -run TestSyncUpserts -v`
Expected: FAIL — `undefined: Sync`.

- [ ] **Step 3: Implement sync + the shared `ifacePHash` helper**

```go
package catalog

import (
    "context"; "time"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/phash"
    "github.com/starkweb/promhash/internal/promclient"
)

// ifacePHash is the canonical interface identity (device + canonical ifName).
func ifacePHash(device, canonicalIfName string) string {
    return phash.Hash(phash.KindIface, device, canonicalIfName)
}

// Sync turns harvested Prometheus rows into Interface nodes, keyed by canonical
// (device, ifName), binding the real metric labels and current ifIndex.
func Sync(ctx context.Context, r *graph.Repo, rows []promclient.IfaceRow,
    devByInstance map[string]string, vendor string) error {
    now := time.Now().UTC()
    for _, row := range rows {
        device := devByInstance[row.Instance]
        if device == "" { device = row.Instance } // fall back to instance if unmapped
        canon := CanonicalIfName(vendor, row.IfName)
        if canon == "" { canon = CanonicalIfName(vendor, row.IfDescr) }
        ifc := graph.Iface{
            PHash: ifacePHash(device, canon), Device: device, IfName: canon,
            MetricIfName: row.IfName, IfDescr: row.IfDescr, IfAlias: row.IfAlias,
            Instance: row.Instance, Vendor: vendor, IfIndex: row.IfIndex, ObservedAt: now}
        if err := r.UpsertInterface(ctx, ifc); err != nil { return err }
    }
    return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/catalog/ -run TestSyncUpserts -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/catalog/sync.go internal/catalog/sync_test.go
git commit -m "feat: catalog sync harvests interfaces into the graph (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 3.6: `cmd/promhash-catalog` CLI

**Files:**
- Create: `cmd/promhash-catalog/main.go`

- [ ] **Step 1: Write a smoke test via build**

(No unit test — this is wiring. Verified by `go build` and a `--help` run.)

- [ ] **Step 2: Implement the CLI**

```go
package main

import (
    "context"; "flag"; "log"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    "github.com/starkweb/promhash/internal/catalog"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/nautobot"
    "github.com/starkweb/promhash/internal/promclient"
)

func main() {
    var promURL, neoURL, neoUser, neoPass, nbURL, nbToken, vendor string
    flag.StringVar(&promURL, "prometheus", "http://localhost:9090", "Prometheus base URL")
    flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "Neo4j bolt URL")
    flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
    flag.StringVar(&neoPass, "neo4j-pass", "", "")
    flag.StringVar(&nbURL, "nautobot", "", "Nautobot base URL")
    flag.StringVar(&nbToken, "nautobot-token", "", "")
    flag.StringVar(&vendor, "vendor", "cisco", "default vendor for canonicalization")
    flag.Parse()
    ctx := context.Background()
    drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
    if err != nil { log.Fatal(err) }
    defer drv.Close(ctx)
    r := graph.New(drv, "neo4j")
    if err := r.EnsureConstraints(ctx); err != nil { log.Fatal(err) }
    pc, err := promclient.New(promURL); if err != nil { log.Fatal(err) }
    rows, err := pc.HarvestInterfaces(ctx); if err != nil { log.Fatal(err) }
    devMap := map[string]string{}
    if nbURL != "" {
        if devMap, err = nautobot.New(nbURL, nbToken).DeviceInstanceMap(ctx); err != nil { log.Fatal(err) }
    }
    inv := map[string]string{}
    for dev, ip := range devMap { inv[ip] = dev } // instance(ip) -> device
    if err := catalog.Sync(ctx, r, rows, inv, vendor); err != nil { log.Fatal(err) }
    log.Printf("catalog sync: %d interfaces", len(rows))
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/promhash-catalog/ && ./promhash-catalog -h`
Expected: usage prints; exit 0.

- [ ] **Step 4: Clean the binary**

Run: `rm -f promhash-catalog`

- [ ] **Step 5: Commit**

```bash
git add cmd/promhash-catalog/
git commit -m "feat: promhash-catalog sync CLI (C9)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 4 — C4 Declared-path loader (`internal/declare`)

### Task 4.1: YAML types + parse

**Files:**
- Create: `internal/declare/types.go`, `internal/declare/types_test.go`

- [ ] **Step 1: Write the failing test**

```go
package declare

import "testing"

const sample = `
app: payments
runs_as: payments-api
owner: team-payments
consumed_by_customers: [acme, globex]
depends_on:
  - to: ledger-api
    paths:
      - hops:
          - {device: rtr-acc-fra-1, if: Te0/1/2, direction: egress}
          - {device: rtr-core-1, if: "uplink-ledger-dc", direction: transit}
`

func TestParse(t *testing.T) {
    d, err := Parse([]byte(sample))
    if err != nil { t.Fatal(err) }
    if d.App != "payments" || d.RunsAs != "payments-api" { t.Fatalf("got %+v", d) }
    if len(d.DependsOn) != 1 || d.DependsOn[0].To != "ledger-api" { t.Fatal("dep parse") }
    if len(d.DependsOn[0].Paths[0].Hops) != 2 { t.Fatal("hops parse") }
    if d.DependsOn[0].Paths[0].Hops[0].If != "Te0/1/2" { t.Fatal("if parse") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/declare/ -run TestParse -v`
Expected: FAIL — `undefined: Parse`.

- [ ] **Step 3: Implement types + Parse**

```go
package declare

import "gopkg.in/yaml.v3"

type Hop struct {
    Device    string `yaml:"device"`
    If        string `yaml:"if"`
    Direction string `yaml:"direction"`
}
type Path struct { Hops []Hop `yaml:"hops"` }
type Dependency struct {
    To    string `yaml:"to"`
    Path  *Path  `yaml:"path"`  // sugar: single candidate
    Paths []Path `yaml:"paths"` // candidate set
}
type App struct {
    App                 string       `yaml:"app"`
    RunsAs              string       `yaml:"runs_as"`
    Owner               string       `yaml:"owner"`
    ConsumedByCustomers []string     `yaml:"consumed_by_customers"`
    DependsOn           []Dependency `yaml:"depends_on"`
}

// Candidates normalizes the `path`/`paths` sugar into a single slice.
func (d Dependency) Candidates() []Path {
    if d.Path != nil { return append([]Path{*d.Path}, d.Paths...) }
    return d.Paths
}

func Parse(b []byte) (App, error) {
    var a App
    err := yaml.Unmarshal(b, &a)
    return a, err
}
```

Run: `go get gopkg.in/yaml.v3`

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/declare/ -run TestParse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/declare/types.go internal/declare/types_test.go go.mod go.sum
git commit -m "feat: declared-path YAML types and parser (C4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 4.2: Validation gate (resolve every `if:`)

**Files:**
- Create: `internal/declare/validate.go`, `internal/declare/validate_test.go`

- [ ] **Step 1: Write the failing test**

```go
package declare

import ( "testing"; "github.com/starkweb/promhash/internal/catalog"; "github.com/starkweb/promhash/internal/graph" )

func resolver() *catalog.Resolver {
    return catalog.NewResolver([]graph.Iface{
        {PHash: "interface:1", Device: "rtr-acc-fra-1", IfName: "tengige0/1/2", MetricIfName: "Te0/1/2", Vendor: "cisco"},
        {PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/2/1", MetricIfName: "Te0/2/1", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
    })
}

func TestValidateOK(t *testing.T) {
    a, _ := Parse([]byte(sample))
    if errs := Validate(a, resolver()); len(errs) != 0 { t.Fatalf("unexpected errors: %v", errs) }
}

func TestValidateUnknownInterfaceFails(t *testing.T) {
    a, _ := Parse([]byte(sample))
    a.DependsOn[0].Paths[0].Hops[0].If = "Te9/9/9"
    errs := Validate(a, resolver())
    if len(errs) == 0 { t.Fatal("expected a validation error for unknown interface") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/declare/ -run TestValidate -v`
Expected: FAIL — `undefined: Validate`.

- [ ] **Step 3: Implement validation**

```go
package declare

import ( "fmt"; "github.com/starkweb/promhash/internal/catalog" )

// Validate resolves every hop's (device, if) against the catalog. Returns one
// error per unresolved/ambiguous reference — the CI gate fails if non-empty.
func Validate(a App, r *catalog.Resolver) []error {
    var errs []error
    for _, dep := range a.DependsOn {
        for pi, p := range dep.Candidates() {
            for _, h := range p.Hops {
                if _, err := r.Resolve(h.Device, h.If); err != nil {
                    errs = append(errs, fmt.Errorf("app %q dep %q path[%d]: %w", a.App, dep.To, pi, err))
                }
            }
        }
    }
    return errs
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/declare/ -run TestValidate -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/declare/validate.go internal/declare/validate_test.go
git commit -m "feat: declaration validation gate (C4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 4.3: Load declaration into the graph

**Files:**
- Create: `internal/declare/load.go`, `internal/declare/load_test.go`
- Modify: `internal/graph/repo.go` (add `UpsertDeclaredApp`)

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package declare

import ( "context"; "testing"; "time"
    "github.com/starkweb/promhash/internal/catalog"; "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/testutil" )

func TestLoadCreatesPathHops(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := graph.New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    // seed catalog interfaces the declaration references
    seedCatalog(t, ctx, r)
    a, _ := Parse([]byte(sample))
    res := catalog.NewResolver(loadCatalog(t, ctx, r))
    if errs := Validate(a, res); len(errs) != 0 { t.Fatalf("validate: %v", errs) }
    if err := Load(ctx, r, a, res, "deadbeef", time.Unix(1700000000, 0).UTC()); err != nil { t.Fatal(err) }
    hops, err := r.AppPath(ctx, appPHash("payments"), time.Now())
    if err != nil { t.Fatal(err) }
    if len(hops) == 0 { t.Fatal("expected hops on payments path") }
}
```

(`seedCatalog`, `loadCatalog`, `appPHash` are small helpers in the test file: `seedCatalog` upserts the two interfaces from `resolver()`; `loadCatalog` lists them back; `appPHash` mirrors the production helper.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/declare/ -run TestLoadCreatesPathHops -v`
Expected: FAIL — `undefined: Load`.

- [ ] **Step 3: Implement `UpsertDeclaredApp` in graph + `Load`**

Add to `internal/graph/repo.go`:
```go
import "time" // ensure imported

// DeclaredHop is a resolved hop ready to persist.
type DeclaredHop struct{ IfacePHash string; Seq int; Direction string }
type DeclaredPath struct{ Hops []DeclaredHop }
type DeclaredDep struct{ ToAppSvc, ToName string; Paths []DeclaredPath }
type DeclaredApp struct {
    AppPHash, App, AppSvcPHash, AppSvc, Owner string
    Customers []string
    Deps      []DeclaredDep
    Source    string
    ValidFrom time.Time
}

func (r *Repo) UpsertDeclaredApp(ctx context.Context, d DeclaredApp) error {
    return r.write(ctx,
        `MERGE (app:Application {phash:$appPHash}) SET app.name=$app, app.owner=$owner
         MERGE (svc:ApplicationService {phash:$svcPHash}) SET svc.name=$appSvc
         MERGE (app)-[:RUNS_AS]->(svc)
         WITH svc
         UNWIND $customers AS cust
           MERGE (c:Customer {phash:'customer:'+cust}) SET c.name=cust
           MERGE (bs:BusinessService {phash:'businessservice:'+cust+':'+$app}) SET bs.name=$app
           MERGE (c)-[:CONSUMES]->(bs) MERGE (bs)-[:REALIZED_BY]->(svc)
         WITH svc
         UNWIND $deps AS dep
           MERGE (target:ApplicationService {phash:dep.toPHash}) SET target.name=dep.to
           MERGE (svc)-[do:DEPENDS_ON]->(target)
             SET do.provenance='declared', do.source=$source, do.validFrom=$validFrom, do.validTo=null
           CREATE (conn:Connection {provenance:'declared', source:$source, validFrom:$validFrom, validTo:null})
           MERGE (svc)-[:USES]->(conn) MERGE (conn)-[:TO_SVC]->(target)
           WITH conn, dep
           UNWIND dep.paths AS p
             CREATE (path:Path {provenance:'declared', source:$source})
             MERGE (conn)-[:TAKES {provenance:'declared', source:$source, validFrom:$validFrom, validTo:null}]->(path)
             WITH path, p
             UNWIND p.hops AS h
               MATCH (iface:Interface {phash:h.ifacePHash})
               MERGE (path)-[:HOP {seq:h.seq, direction:h.direction}]->(iface)`,
        map[string]any{
            "appPHash": d.AppPHash, "app": d.App, "owner": d.Owner,
            "svcPHash": d.AppSvcPHash, "appSvc": d.AppSvc, "customers": d.Customers,
            "source": d.Source, "validFrom": d.ValidFrom.Unix(), "deps": depsToParams(d.Deps),
        })
}

func depsToParams(deps []DeclaredDep) []map[string]any {
    out := make([]map[string]any, 0, len(deps))
    for _, dep := range deps {
        paths := make([]map[string]any, 0, len(dep.Paths))
        for _, p := range dep.Paths {
            hops := make([]map[string]any, 0, len(p.Hops))
            for _, h := range p.Hops {
                hops = append(hops, map[string]any{"ifacePHash": h.IfacePHash, "seq": h.Seq, "direction": h.Direction})
            }
            paths = append(paths, map[string]any{"hops": hops})
        }
        out = append(out, map[string]any{"toPHash": dep.ToAppSvc, "to": dep.ToName, "paths": paths})
    }
    return out
}
```

Create `internal/declare/load.go`:
```go
package declare

import (
    "context"; "fmt"; "time"
    "github.com/starkweb/promhash/internal/catalog"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/phash"
)

func appPHash(app string) string    { return phash.Hash(phash.KindApp, app) }
func appSvcPHash(svc string) string { return phash.Hash(phash.KindAppSvc, svc) }

// Load resolves and persists a declaration. Assumes Validate already passed.
func Load(ctx context.Context, r *graph.Repo, a App, res *catalog.Resolver, source string, validFrom time.Time) error {
    da := graph.DeclaredApp{
        AppPHash: appPHash(a.App), App: a.App, AppSvcPHash: appSvcPHash(a.RunsAs), AppSvc: a.RunsAs,
        Owner: a.Owner, Customers: a.ConsumedByCustomers, Source: source, ValidFrom: validFrom,
    }
    for _, dep := range a.DependsOn {
        gd := graph.DeclaredDep{ToAppSvc: appSvcPHash(dep.To), ToName: dep.To}
        for _, p := range dep.Candidates() {
            var gp graph.DeclaredPath
            for seq, h := range p.Hops {
                ifc, err := res.Resolve(h.Device, h.If)
                if err != nil { return fmt.Errorf("load %s: %w", a.App, err) }
                gp.Hops = append(gp.Hops, graph.DeclaredHop{IfacePHash: ifc.PHash, Seq: seq + 1, Direction: h.Direction})
            }
            gd.Paths = append(gd.Paths, gp)
        }
        da.Deps = append(da.Deps, gd)
    }
    return r.UpsertDeclaredApp(ctx, da)
}
```

(`AppPath` is implemented in Task 6.1; this test depends on it — implement Task 6.1's `AppPath` query first if running tests strictly in order, or stub it to return rows here. The plan orders 6.1 after 4.x; when executing, jump to add `AppPath` before running this integration test.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/declare/ -run TestLoadCreatesPathHops -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/declare/load.go internal/declare/load_test.go internal/graph/repo.go
git commit -m "feat: load declarations into the graph as Connection/Path/Hop (C4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 4.4: Retraction closes validity (no hard delete)

**Files:**
- Modify: `internal/graph/repo.go` (add `CloseAppValidity`)
- Modify: `internal/declare/load_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
func TestCloseValiditySetsValidTo(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := graph.New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    seedCatalog(t, ctx, r)
    a, _ := Parse([]byte(sample)); res := catalog.NewResolver(loadCatalog(t, ctx, r))
    _ = Load(ctx, r, a, res, "sha1", time.Unix(1700000000, 0).UTC())
    closeAt := time.Unix(1700000900, 0).UTC()
    if err := r.CloseAppValidity(ctx, appPHash("payments"), closeAt); err != nil { t.Fatal(err) }
    hops, _ := r.AppPath(ctx, appPHash("payments"), time.Now())
    if len(hops) != 0 { t.Fatalf("expected no current hops after retraction, got %d", len(hops)) }
    past, _ := r.AppPath(ctx, appPHash("payments"), time.Unix(1700000100, 0).UTC())
    if len(past) == 0 { t.Fatal("expected historical hops to remain queryable") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/declare/ -run TestCloseValidity -v`
Expected: FAIL — `undefined: (*Repo).CloseAppValidity`.

- [ ] **Step 3: Implement `CloseAppValidity`**

```go
// append to internal/graph/repo.go
func (r *Repo) CloseAppValidity(ctx context.Context, appPHash string, at time.Time) error {
    return r.write(ctx,
        `MATCH (app:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(conn:Connection)-[t:TAKES]->(:Path)
         WHERE t.validTo IS NULL SET t.validTo=$at, conn.validTo=$at
         WITH svc
         MATCH (svc)-[do:DEPENDS_ON]->() WHERE do.validTo IS NULL SET do.validTo=$at`,
        map[string]any{"appPHash": appPHash, "at": at.Unix()})
}
```

Then make reload idempotent — supersede an app's open edges before re-creating. Modify
`internal/declare/load.go`, changing the final return of `Load`:
```go
        da.Deps = append(da.Deps, gd)
    }
    // Supersede any currently-open edges so re-declaration replaces rather than
    // duplicates (Connection/Path are CREATEd). First load is a no-op.
    if err := r.CloseAppValidity(ctx, da.AppPHash, validFrom); err != nil { return err }
    return r.UpsertDeclaredApp(ctx, da)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/declare/ -run TestCloseValidity -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/declare/load.go internal/declare/load_test.go
git commit -m "feat: retraction closes validity interval + idempotent reload (C4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 4.5: `cmd/promhash-loader` (validate + load a directory)

**Files:**
- Create: `cmd/promhash-loader/main.go`

- [ ] **Step 1: Smoke via build (wiring task).**

- [ ] **Step 2: Implement the CLI**

```go
package main

import (
    "context"; "flag"; "log"; "os"; "path/filepath"; "time"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    "github.com/starkweb/promhash/internal/catalog"
    "github.com/starkweb/promhash/internal/declare"
    "github.com/starkweb/promhash/internal/graph"
)

func main() {
    var dir, neoURL, neoUser, neoPass, sha string
    var validateOnly bool
    flag.StringVar(&dir, "dir", "declared", "directory of *.yaml declarations")
    flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
    flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
    flag.StringVar(&neoPass, "neo4j-pass", "", "")
    flag.StringVar(&sha, "source", "manual", "git sha for provenance")
    flag.BoolVar(&validateOnly, "validate-only", false, "CI gate: validate, do not write")
    flag.Parse()
    ctx := context.Background()
    drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
    if err != nil { log.Fatal(err) }
    defer drv.Close(ctx)
    r := graph.New(drv, "neo4j")
    cat, err := r.ListAllInterfaces(ctx); if err != nil { log.Fatal(err) }
    res := catalog.NewResolver(cat)
    files, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
    now := time.Now().UTC()
    var failed bool
    for _, f := range files {
        b, _ := os.ReadFile(f)
        a, err := declare.Parse(b); if err != nil { log.Printf("%s: parse: %v", f, err); failed = true; continue }
        if errs := declare.Validate(a, res); len(errs) != 0 {
            for _, e := range errs { log.Printf("%s: %v", f, e) }; failed = true; continue
        }
        if !validateOnly {
            if err := declare.Load(ctx, r, a, res, sha, now); err != nil { log.Printf("%s: load: %v", f, err); failed = true }
        }
    }
    if failed { os.Exit(1) }
    log.Printf("processed %d declarations (validateOnly=%v)", len(files), validateOnly)
}
```

Add `ListAllInterfaces` to `internal/graph/repo.go`:
```go
func (r *Repo) ListAllInterfaces(ctx context.Context) ([]Iface, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv, `MATCH (n:Interface) RETURN n`, nil,
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil { return nil, err }
    out := make([]Iface, 0, len(res.Records))
    for _, rec := range res.Records { n, _ := rec.Get("n"); out = append(out, ifaceFromProps(n.(neo4j.Node).Props)) }
    return out, nil
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/promhash-loader/ && ./promhash-loader -h && rm -f promhash-loader`
Expected: usage prints.

- [ ] **Step 4: Run unit + vet**

Run: `make test && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/promhash-loader/ internal/graph/repo.go
git commit -m "feat: promhash-loader CLI with CI validate gate (C4)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 5 — C5 Enrichment (`internal/enrich`)

### Task 5.1: `AppPath` traversal (union of candidate paths)

**Files:**
- Modify: `internal/graph/repo.go` (add `AppPath`)
- Create: `internal/graph/apppath_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package graph

import ( "context"; "testing"; "time"; "github.com/starkweb/promhash/internal/testutil" )

func TestAppPathReturnsOrderedUnion(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    // Build a minimal app→svc→conn→path→hop→iface graph for two candidate paths
    seedTwoCandidatePaths(t, ctx, r) // helper inserts iface nodes + the path graph
    hops, err := r.AppPath(ctx, "application:payments", time.Now())
    if err != nil { t.Fatal(err) }
    if len(hops) < 2 { t.Fatalf("want union of >=2 interfaces, got %d", len(hops)) }
    for i := 1; i < len(hops); i++ {
        if hops[i].Seq < hops[i-1].Seq { /* union may interleave paths; ordering is per-path */ }
    }
}
```

(`seedTwoCandidatePaths` writes: an `Application{phash:'application:payments'}`-`RUNS_AS`-`ApplicationService`, `USES` a `Connection`, two `TAKES`→`Path`, each `HOP`→ a distinct `Interface` with `instance`/`ifIndex`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestAppPath -v`
Expected: FAIL — `undefined: (*Repo).AppPath`.

- [ ] **Step 3: Implement `AppPath`**

```go
// append to internal/graph/repo.go
func (r *Repo) AppPath(ctx context.Context, appPHash string, at time.Time) ([]Hop, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv,
        `MATCH (a:Application {phash:$app})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(c:Connection)-[t:TAKES]->(p:Path)-[h:HOP]->(i:Interface)
         WHERE ($at >= t.validFrom) AND (t.validTo IS NULL OR $at < t.validTo)
         RETURN DISTINCT i.device AS device, i.ifName AS ifName, i.metricIfName AS metricIfName,
                i.instance AS instance, i.ifIndex AS ifIndex, h.seq AS seq, h.direction AS direction
         ORDER BY seq`,
        map[string]any{"app": appPHash, "at": at.Unix()},
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil { return nil, err }
    out := make([]Hop, 0, len(res.Records))
    for _, rec := range res.Records {
        gs := func(k string) string { v, _ := rec.Get(k); s, _ := v.(string); return s }
        gi := func(k string) int { v, _ := rec.Get(k); n, _ := v.(int64); return int(n) }
        out = append(out, Hop{Device: gs("device"), IfName: gs("ifName"), MetricIfName: gs("metricIfName"),
            Instance: gs("instance"), IfIndex: gi("ifIndex"), Seq: gi("seq"), Direction: gs("direction"),
            Provenance: "declared"})
    }
    return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestAppPath -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/graph/apppath_test.go
git commit -m "feat: app-path traversal over candidate paths (C5/C7)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 5.2: Federation `match[]` generator

**Files:**
- Create: `internal/enrich/federation.go`, `internal/enrich/federation_test.go`

- [ ] **Step 1: Write the failing test**

```go
package enrich

import ( "testing"; "github.com/starkweb/promhash/internal/graph" )

func TestFederationMatch(t *testing.T) {
    hops := []graph.Hop{
        {Instance: "10.0.0.2", IfIndex: 43}, {Instance: "10.0.0.1", IfIndex: 42}, {Instance: "10.0.0.1", IfIndex: 42},
    }
    got := FederationMatch(hops)
    want := `{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1|10.0.0.2", ifIndex=~"42|43"}`
    if got != want { t.Fatalf("\n got %q\nwant %q", got, want) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestFederationMatch -v`
Expected: FAIL — `undefined: FederationMatch`.

- [ ] **Step 3: Implement (dedup + sort for determinism)**

```go
package enrich

import ( "fmt"; "sort"; "strconv"; "strings"; "github.com/starkweb/promhash/internal/graph" )

func FederationMatch(hops []graph.Hop) string {
    insts, idxs := map[string]struct{}{}, map[string]struct{}{}
    for _, h := range hops {
        insts[h.Instance] = struct{}{}
        idxs[strconv.Itoa(h.IfIndex)] = struct{}{}
    }
    return fmt.Sprintf(`{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"%s", ifIndex=~"%s"}`,
        strings.Join(sortedKeys(insts), "|"), strings.Join(sortedKeys(idxs), "|"))
}

func sortedKeys(m map[string]struct{}) []string {
    out := make([]string, 0, len(m))
    for k := range m { out = append(out, k) }
    sort.Strings(out)
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestFederationMatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/federation.go internal/enrich/federation_test.go
git commit -m "feat: federation match[] generator (C5)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 5.3: Recording-rule generator (direction-aware, per-hop, no cross-sum)

**Files:**
- Create: `internal/enrich/rules.go`, `internal/enrich/rules_test.go`, `internal/enrich/testdata/payments.golden.yaml`

- [ ] **Step 1: Write the failing test (golden file)**

```go
package enrich

import ( "os"; "testing"; "github.com/starkweb/promhash/internal/graph" )

func TestRuleGroupGolden(t *testing.T) {
    hops := []graph.Hop{
        {Device: "rtr-acc-fra-1", MetricIfName: "Te0/1/2", Instance: "10.0.0.5", IfIndex: 7, Direction: "egress"},
        {Device: "rtr-dc-ledger", MetricIfName: "Te1/0/4", Instance: "10.0.0.1", IfIndex: 42, Direction: "ingress"},
    }
    got := RuleGroup("payments", "payments-api", hops)
    want, _ := os.ReadFile("testdata/payments.golden.yaml")
    if got != string(want) { t.Fatalf("golden mismatch:\n%s", got) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestRuleGroupGolden -v`
Expected: FAIL — `undefined: RuleGroup`.

- [ ] **Step 3: Implement + write the golden file**

```go
package enrich

import ( "fmt"; "strings"; "github.com/starkweb/promhash/internal/graph" )

// RuleGroup emits one recording rule per hop (no cross-candidate-path summation),
// choosing ifHCIn/OutOctets by hop direction. coverage=declared stamps provenance.
func RuleGroup(app, service string, hops []graph.Hop) string {
    var b strings.Builder
    fmt.Fprintf(&b, "groups:\n- name: promhash_%s\n  rules:\n", app)
    job := "promhash-fed-" + app
    for _, h := range hops {
        metric, dir := "ifHCInOctets", "ingress"
        if h.Direction == "egress" { metric, dir = "ifHCOutOctets", "egress" }
        fmt.Fprintf(&b,
            "  - record: app:if_%s_octets:rate5m\n    expr: rate(%s{job=\"%s\", instance=\"%s\", ifIndex=\"%d\"}[5m])\n    labels: {app: %s, service: %s, device: %s, ifName: %s, coverage: declared}\n",
            dir, metric, job, h.Instance, h.IfIndex, app, service, h.Device, h.MetricIfName)
    }
    return b.String()
}
```

Write `internal/enrich/testdata/payments.golden.yaml` to match the generator output exactly:
```yaml
groups:
- name: promhash_payments
  rules:
  - record: app:if_egress_octets:rate5m
    expr: rate(ifHCOutOctets{job="promhash-fed-payments", instance="10.0.0.5", ifIndex="7"}[5m])
    labels: {app: payments, service: payments-api, device: rtr-acc-fra-1, ifName: Te0/1/2, coverage: declared}
  - record: app:if_ingress_octets:rate5m
    expr: rate(ifHCInOctets{job="promhash-fed-payments", instance="10.0.0.1", ifIndex="42"}[5m])
    labels: {app: payments, service: payments-api, device: rtr-dc-ledger, ifName: Te1/0/4, coverage: declared}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestRuleGroupGolden -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/rules.go internal/enrich/rules_test.go internal/enrich/testdata/
git commit -m "feat: direction-aware per-hop recording-rule generator (C5)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 5.4: `cmd/promhash-enrich` (graph → artifacts)

**Files:**
- Create: `cmd/promhash-enrich/main.go`

- [ ] **Step 1: Smoke via build (wiring).**

- [ ] **Step 2: Implement**

```go
package main

import (
    "context"; "flag"; "log"; "os"; "path/filepath"; "strings"; "time"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    "github.com/starkweb/promhash/internal/enrich"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/phash"
)

func main() {
    var neoURL, neoUser, neoPass, outDir, allowlist string
    flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
    flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
    flag.StringVar(&neoPass, "neo4j-pass", "", "")
    flag.StringVar(&outDir, "out", "gitops/enrichment", "output dir for artifacts")
    flag.StringVar(&allowlist, "apps", "", "comma-separated curated app names")
    flag.Parse()
    ctx := context.Background()
    drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
    if err != nil { log.Fatal(err) }
    defer drv.Close(ctx)
    r := graph.New(drv, "neo4j")
    for _, app := range strings.Split(allowlist, ",") {
        app = strings.TrimSpace(app); if app == "" { continue }
        hops, err := r.AppPath(ctx, phash.Hash(phash.KindApp, app), time.Now())
        if err != nil { log.Fatal(err) }
        if len(hops) == 0 { log.Printf("WARN app %q has no known path; skipping", app); continue }
        svc, _ := r.AppServiceName(ctx, app)
        dir := filepath.Join(outDir, app); _ = os.MkdirAll(dir, 0o755)
        _ = os.WriteFile(filepath.Join(dir, "federate.match"), []byte(enrich.FederationMatch(hops)+"\n"), 0o644)
        _ = os.WriteFile(filepath.Join(dir, "rules.yaml"), []byte(enrich.RuleGroup(app, svc, hops)), 0o644)
        log.Printf("app %q: %d hops -> %s", app, len(hops), dir)
    }
}
```

Add `AppServiceName` to `internal/graph/repo.go`:
```go
func (r *Repo) AppServiceName(ctx context.Context, app string) (string, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv,
        `MATCH (:Application {name:$app})-[:RUNS_AS]->(s:ApplicationService) RETURN s.name AS n LIMIT 1`,
        map[string]any{"app": app}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil || len(res.Records) == 0 { return app, err }
    v, _ := res.Records[0].Get("n"); s, _ := v.(string); return s, nil
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./cmd/promhash-enrich/ && ./promhash-enrich -h && rm -f promhash-enrich`

- [ ] **Step 4: Run unit + vet**

Run: `make test && go vet ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/promhash-enrich/ internal/graph/repo.go
git commit -m "feat: promhash-enrich generates federation + rule artifacts (C5)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 6 — C6 Federation/tenant artifacts

### Task 6.1: Tenant scrape-config generator + deploy doc

**Files:**
- Create: `internal/enrich/tenant.go`, `internal/enrich/tenant_test.go`, `internal/enrich/testdata/tenant.golden.yaml`
- Create: `docs/deploy/federation-tenant.md`

- [ ] **Step 1: Write the failing test (golden)**

```go
package enrich

import ( "os"; "testing" )

func TestTenantScrapeConfigGolden(t *testing.T) {
    got := TenantScrapeConfig("payments", "http://main-prometheus:9090",
        `{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1", ifIndex=~"42"}`)
    want, _ := os.ReadFile("testdata/tenant.golden.yaml")
    if got != string(want) { t.Fatalf("golden mismatch:\n%s", got) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/enrich/ -run TestTenantScrapeConfig -v`
Expected: FAIL — `undefined: TenantScrapeConfig`.

- [ ] **Step 3: Implement + golden + deploy doc**

```go
// internal/enrich/tenant.go
package enrich

import "fmt"

// TenantScrapeConfig is the per-app federation scrape job: pull only this app's
// slice from the main Prometheus into the curated tenant.
func TenantScrapeConfig(app, mainProm, match string) string {
    return fmt.Sprintf(`scrape_configs:
  - job_name: promhash-fed-%s
    honor_labels: true
    metrics_path: /federate
    params:
      'match[]': ['%s']
    static_configs:
      - targets: ['%s']
`, app, match, hostPort(mainProm))
}

func hostPort(u string) string {
    s := u
    for _, p := range []string{"http://", "https://"} {
        if len(s) >= len(p) && s[:len(p)] == p { s = s[len(p):] }
    }
    return s
}
```

Write `internal/enrich/testdata/tenant.golden.yaml`:
```yaml
scrape_configs:
  - job_name: promhash-fed-payments
    honor_labels: true
    metrics_path: /federate
    params:
      'match[]': ['{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1", ifIndex=~"42"}']
    static_configs:
      - targets: ['main-prometheus:9090']
```

Write `docs/deploy/federation-tenant.md` describing: one logical tenant per curated app; the tenant Prometheus loads `federate.match`-derived scrape config + `rules.yaml`; it `remote_write`s to the cloud LTS with an `app`-scoped external label; GitOps (the existing pipeline) applies `gitops/enrichment/<app>/`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/enrich/ -run TestTenantScrapeConfig -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/enrich/tenant.go internal/enrich/tenant_test.go internal/enrich/testdata/tenant.golden.yaml docs/deploy/federation-tenant.md
git commit -m "feat: per-app federation tenant scrape-config generator (C6)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 7 — C7 API (`internal/api`)

### Task 7.1: Impact + interface-apps queries in the repo

**Files:**
- Modify: `internal/graph/repo.go` (add `InterfaceImpact`)
- Create: `internal/graph/impact_test.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package graph

import ( "context"; "testing"; "time"; "github.com/starkweb/promhash/internal/testutil" )

func TestInterfaceImpact(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    seedTwoCandidatePaths(t, ctx, r) // reused helper: payments traverses interface:a
    rows, err := r.InterfaceImpact(ctx, "interface:a", time.Now())
    if err != nil { t.Fatal(err) }
    if len(rows) == 0 || rows[0].App == "" { t.Fatalf("expected impacted app, got %+v", rows) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestInterfaceImpact -v`
Expected: FAIL — `undefined: (*Repo).InterfaceImpact`.

- [ ] **Step 3: Implement reverse traversal**

```go
// append to internal/graph/repo.go
func (r *Repo) InterfaceImpact(ctx context.Context, ifacePHash string, at time.Time) ([]ImpactRow, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv,
        `MATCH (i:Interface {phash:$if})<-[:HOP]-(:Path)<-[t:TAKES]-(:Connection)<-[:USES]-(svc:ApplicationService)
         WHERE ($at >= t.validFrom) AND (t.validTo IS NULL OR $at < t.validTo)
         MATCH (a:Application)-[:RUNS_AS]->(svc)
         OPTIONAL MATCH (svc)<-[:REALIZED_BY]-(:BusinessService)<-[:CONSUMES]-(c:Customer)
         RETURN DISTINCT a.name AS app, svc.name AS service, a.owner AS owner,
                coalesce(c.name,'') AS customer, coalesce(a.criticality,'') AS criticality`,
        map[string]any{"if": ifacePHash, "at": at.Unix()},
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil { return nil, err }
    out := make([]ImpactRow, 0, len(res.Records))
    for _, rec := range res.Records {
        gs := func(k string) string { v, _ := rec.Get(k); s, _ := v.(string); return s }
        out = append(out, ImpactRow{App: gs("app"), Service: gs("service"), Owner: gs("owner"),
            Customer: gs("customer"), Criticality: gs("criticality")})
    }
    return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -tags=integration ./internal/graph/ -run TestInterfaceImpact -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go internal/graph/impact_test.go
git commit -m "feat: interface impact reverse-traversal (C7)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 7.2: HTTP handlers + server

**Files:**
- Create: `internal/api/server.go`, `internal/api/server_test.go`

- [ ] **Step 1: Write the failing test (handler over a fake repo)**

```go
package api

import ( "context"; "encoding/json"; "net/http"; "net/http/httptest"; "testing"; "time"
    "github.com/starkweb/promhash/internal/graph" )

type fakeRepo struct{}
func (fakeRepo) AppPath(_ context.Context, app string, _ time.Time) ([]graph.Hop, error) {
    return []graph.Hop{{Device: "rtr-core-1", MetricIfName: "Te0/1/2", IfIndex: 42, Direction: "egress"}}, nil
}
func (fakeRepo) InterfaceImpact(_ context.Context, _ string, _ time.Time) ([]graph.ImpactRow, error) {
    return []graph.ImpactRow{{App: "payments", Service: "payments-api"}}, nil
}
func (fakeRepo) ListApps(_ context.Context) ([]string, error) { return []string{"payments"}, nil }

func TestAppPathHandler(t *testing.T) {
    srv := NewServer(fakeRepo{})
    rec := httptest.NewRecorder()
    req := httptest.NewRequest(http.MethodGet, "/apps/payments/path", nil)
    srv.Mux().ServeHTTP(rec, req)
    if rec.Code != 200 { t.Fatalf("code %d", rec.Code) }
    var out []map[string]any
    _ = json.Unmarshal(rec.Body.Bytes(), &out)
    if len(out) != 1 || out[0]["ifIndex"].(float64) != 42 { t.Fatalf("body %s", rec.Body) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/api/ -run TestAppPathHandler -v`
Expected: FAIL — `undefined: NewServer`.

- [ ] **Step 3: Implement server + handlers**

```go
package api

import (
    "context"; "encoding/json"; "net/http"; "strconv"; "time"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/phash"
)

type Repo interface {
    AppPath(ctx context.Context, appPHash string, at time.Time) ([]graph.Hop, error)
    InterfaceImpact(ctx context.Context, ifacePHash string, at time.Time) ([]graph.ImpactRow, error)
    ListApps(ctx context.Context) ([]string, error)
}

type Server struct{ repo Repo; mux *http.ServeMux }

func NewServer(r Repo) *Server {
    s := &Server{repo: r, mux: http.NewServeMux()}
    s.mux.HandleFunc("GET /apps", s.listApps)
    s.mux.HandleFunc("GET /apps/{app}/path", s.appPath)
    s.mux.HandleFunc("GET /interfaces/{device}/{ifName}/apps", s.ifaceApps)
    s.mux.HandleFunc("GET /impact", s.impact)
    return s
}
func (s *Server) Mux() *http.ServeMux { return s.mux }

func at(r *http.Request) time.Time {
    if v := r.URL.Query().Get("at"); v != "" {
        if sec, err := strconv.ParseInt(v, 10, 64); err == nil { return time.Unix(sec, 0).UTC() }
    }
    return time.Now()
}
func writeJSON(w http.ResponseWriter, v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }

func (s *Server) listApps(w http.ResponseWriter, r *http.Request) {
    apps, err := s.repo.ListApps(r.Context()); if err != nil { http.Error(w, err.Error(), 500); return }
    writeJSON(w, apps)
}
func (s *Server) appPath(w http.ResponseWriter, r *http.Request) {
    hops, err := s.repo.AppPath(r.Context(), phash.Hash(phash.KindApp, r.PathValue("app")), at(r))
    if err != nil { http.Error(w, err.Error(), 500); return }
    if hops == nil { hops = []graph.Hop{} } // explicit empty, never null
    writeJSON(w, hops)
}
func (s *Server) ifaceApps(w http.ResponseWriter, r *http.Request) {
    p := phash.Hash(phash.KindIface, r.PathValue("device"), r.PathValue("ifName"))
    rows, err := s.repo.InterfaceImpact(r.Context(), p, at(r))
    if err != nil { http.Error(w, err.Error(), 500); return }
    writeJSON(w, rows)
}
func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
    device, ifName := r.URL.Query().Get("device"), r.URL.Query().Get("ifName")
    p := phash.Hash(phash.KindIface, device, ifName)
    rows, err := s.repo.InterfaceImpact(r.Context(), p, at(r))
    if err != nil { http.Error(w, err.Error(), 500); return }
    if len(rows) == 0 { writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": []graph.ImpactRow{}, "note": "no path known"}); return }
    writeJSON(w, map[string]any{"interface": device + "/" + ifName, "impact": rows})
}
```

Note: the `ifName` path/query value must be the **canonical** form; the Grafana plugin and callers pass canonical names produced by C9. (Document this in the handler comment.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/api/ -run TestAppPathHandler -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/api/
git commit -m "feat: promhash HTTP API handlers (C7)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 7.3: `ListApps` repo method + `cmd/promhash-api`

**Files:**
- Modify: `internal/graph/repo.go` (add `ListApps`)
- Create: `cmd/promhash-api/main.go`

- [ ] **Step 1: Write the failing test**

```go
//go:build integration
package graph

import ( "context"; "testing"; "github.com/starkweb/promhash/internal/testutil" )

func TestListApps(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    _ = r.write(ctx, `CREATE (:Application {phash:'application:payments', name:'payments'})`, nil)
    apps, err := r.ListApps(ctx)
    if err != nil { t.Fatal(err) }
    if len(apps) != 1 || apps[0] != "payments" { t.Fatalf("got %v", apps) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestListApps -v`
Expected: FAIL — `undefined: (*Repo).ListApps`.

- [ ] **Step 3: Implement `ListApps` + the API main**

```go
// append to internal/graph/repo.go
func (r *Repo) ListApps(ctx context.Context) ([]string, error) {
    res, err := neo4j.ExecuteQuery(ctx, r.drv,
        `MATCH (a:Application) RETURN a.name AS n ORDER BY n`, nil,
        neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
    if err != nil { return nil, err }
    out := make([]string, 0, len(res.Records))
    for _, rec := range res.Records { v, _ := rec.Get("n"); s, _ := v.(string); out = append(out, s) }
    return out, nil
}
```

```go
// cmd/promhash-api/main.go
package main

import (
    "context"; "flag"; "log"; "net/http"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    "github.com/starkweb/promhash/internal/api"
    "github.com/starkweb/promhash/internal/graph"
)

func main() {
    var addr, neoURL, neoUser, neoPass string
    flag.StringVar(&addr, "addr", ":8080", "")
    flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
    flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
    flag.StringVar(&neoPass, "neo4j-pass", "", "")
    flag.Parse()
    ctx := context.Background()
    drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
    if err != nil { log.Fatal(err) }
    defer drv.Close(ctx)
    srv := api.NewServer(graph.New(drv, "neo4j"))
    log.Printf("promhash-api listening on %s", addr)
    log.Fatal(http.ListenAndServe(addr, srv.Mux()))
}
```

- [ ] **Step 4: Run tests + build**

Run: `go test -tags=integration ./internal/graph/ -run TestListApps -v && go build ./cmd/promhash-api/ && rm -f promhash-api`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go cmd/promhash-api/
git commit -m "feat: list apps + promhash-api server entrypoint (C7)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 8 — C3 Seed importers (`internal/servicenow`, `cmd/promhash-seed`)

### Task 8.1: ServiceNow app→CI→IP client

**Files:**
- Create: `internal/servicenow/servicenow.go`, `internal/servicenow/servicenow_test.go`

- [ ] **Step 1: Write the failing test**

```go
package servicenow

import ( "context"; "net/http"; "net/http/httptest"; "testing" )

func TestApplications(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Write([]byte(`{"result":[{"name":"payments","sys_id":"abc","u_app_service":"payments-api"}]}`))
    }))
    defer srv.Close()
    c := New(srv.URL, "user", "pass")
    apps, err := c.Applications(context.Background())
    if err != nil { t.Fatal(err) }
    if len(apps) != 1 || apps[0].Name != "payments" || apps[0].Service != "payments-api" { t.Fatalf("got %+v", apps) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/servicenow/ -v`
Expected: FAIL — `undefined: New`.

- [ ] **Step 3: Implement**

```go
package servicenow

import ( "context"; "encoding/json"; "net/http"; "strings"; "time" )

type Client struct{ base, user, pass string; hc *http.Client }
func New(base, user, pass string) *Client {
    return &Client{base: strings.TrimRight(base, "/"), user: user, pass: pass, hc: &http.Client{Timeout: 30 * time.Second}}
}
type Application struct{ Name, SysID, Service string }
type appResp struct {
    Result []struct {
        Name    string `json:"name"`
        SysID   string `json:"sys_id"`
        Service string `json:"u_app_service"`
    } `json:"result"`
}
func (c *Client) Applications(ctx context.Context) ([]Application, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/now/table/cmdb_ci_appl", nil)
    req.SetBasicAuth(c.user, c.pass)
    resp, err := c.hc.Do(req); if err != nil { return nil, err }
    defer resp.Body.Close()
    var ar appResp
    if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil { return nil, err }
    out := make([]Application, 0, len(ar.Result))
    for _, a := range ar.Result { out = append(out, Application{Name: a.Name, SysID: a.SysID, Service: a.Service}) }
    return out, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/servicenow/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/servicenow/
git commit -m "feat: servicenow application seed client (C3)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 8.2: `cmd/promhash-seed` (one-shot typed-node import)

**Files:**
- Create: `cmd/promhash-seed/main.go`
- Modify: `internal/graph/repo.go` (add `UpsertAppSeed`)

- [ ] **Step 1: Write the failing test for `UpsertAppSeed`**

```go
//go:build integration
package graph

import ( "context"; "testing"; "github.com/starkweb/promhash/internal/testutil" )

func TestUpsertAppSeed(t *testing.T) {
    ctx := context.Background()
    drv, cleanup := testutil.Neo4j(t, ctx); defer cleanup()
    r := New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    if err := r.UpsertAppSeed(ctx, "application:payments", "payments", "appservice:payments-api", "payments-api", "abc"); err != nil { t.Fatal(err) }
    apps, _ := r.ListApps(ctx)
    if len(apps) != 1 { t.Fatalf("got %v", apps) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertAppSeed -v`
Expected: FAIL — `undefined: (*Repo).UpsertAppSeed`.

- [ ] **Step 3: Implement seed upsert + CLI**

```go
// append to internal/graph/repo.go
func (r *Repo) UpsertAppSeed(ctx context.Context, appPHash, app, svcPHash, svc, sysID string) error {
    return r.write(ctx,
        `MERGE (a:Application {phash:$appPHash}) SET a.name=$app, a.sysId=$sysID
         MERGE (s:ApplicationService {phash:$svcPHash}) SET s.name=$svc
         MERGE (a)-[:RUNS_AS]->(s)`,
        map[string]any{"appPHash": appPHash, "app": app, "svcPHash": svcPHash, "svc": svc, "sysID": sysID})
}
```

```go
// cmd/promhash-seed/main.go
package main

import (
    "context"; "flag"; "log"
    "github.com/neo4j/neo4j-go-driver/v5/neo4j"
    "github.com/starkweb/promhash/internal/graph"
    "github.com/starkweb/promhash/internal/phash"
    "github.com/starkweb/promhash/internal/servicenow"
)

func main() {
    var neoURL, neoUser, neoPass, snURL, snUser, snPass string
    flag.StringVar(&neoURL, "neo4j", "bolt://localhost:7687", "")
    flag.StringVar(&neoUser, "neo4j-user", "neo4j", "")
    flag.StringVar(&neoPass, "neo4j-pass", "", "")
    flag.StringVar(&snURL, "servicenow", "", "")
    flag.StringVar(&snUser, "servicenow-user", "", "")
    flag.StringVar(&snPass, "servicenow-pass", "", "")
    flag.Parse()
    ctx := context.Background()
    drv, err := neo4j.NewDriverWithContext(neoURL, neo4j.BasicAuth(neoUser, neoPass, ""))
    if err != nil { log.Fatal(err) }
    defer drv.Close(ctx)
    r := graph.New(drv, "neo4j"); _ = r.EnsureConstraints(ctx)
    apps, err := servicenow.New(snURL, snUser, snPass).Applications(ctx)
    if err != nil { log.Fatal(err) }
    for _, a := range apps {
        _ = r.UpsertAppSeed(ctx, phash.Hash(phash.KindApp, a.Name), a.Name,
            phash.Hash(phash.KindAppSvc, a.Service), a.Service, a.SysID)
    }
    log.Printf("seeded %d applications", len(apps))
}
```

- [ ] **Step 4: Run tests + build**

Run: `go test -tags=integration ./internal/graph/ -run TestUpsertAppSeed -v && go build ./cmd/promhash-seed/ && rm -f promhash-seed`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add internal/graph/repo.go cmd/promhash-seed/
git commit -m "feat: promhash-seed imports typed nodes from servicenow (C3)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Phase 9 — C8 Grafana datasource plugin (`plugin/promhash-datasource`)

### Task 9.1: Plugin scaffold + metadata

**Files:**
- Create: `plugin/promhash-datasource/src/plugin.json`, `plugin/promhash-datasource/go.mod`, `plugin/promhash-datasource/Magefile.go`, `plugin/promhash-datasource/package.json`

- [ ] **Step 1: Create plugin module + metadata**

```bash
cd plugin/promhash-datasource
go mod init github.com/starkweb/promhash-datasource
go get github.com/grafana/grafana-plugin-sdk-go
```

`src/plugin.json`:
```json
{
  "type": "datasource",
  "name": "promhash",
  "id": "starkweb-promhash-datasource",
  "backend": true,
  "executable": "gpx_promhash",
  "info": { "description": "Query the promhash application-path graph", "version": "0.1.0" },
  "dependencies": { "grafanaDependency": ">=10.4.0" }
}
```

`package.json` (frontend deps — minimal):
```json
{ "name": "promhash-datasource", "version": "0.1.0",
  "scripts": { "build": "webpack --mode production" },
  "dependencies": { "@grafana/data": "^10.4.0", "@grafana/runtime": "^10.4.0", "@grafana/ui": "^10.4.0", "react": "^18.2.0" } }
```

- [ ] **Step 2: Verify the plugin module builds**

Run: `cd plugin/promhash-datasource && go build ./... ; cd -`
Expected: exit 0 (no Go files yet → no-op build is fine).

- [ ] **Step 3: Commit**

```bash
git add plugin/promhash-datasource/
git commit -m "chore: scaffold grafana datasource plugin (C8)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 9.2: Backend `CheckHealth` (verify C7 reachable)

**Files:**
- Create: `plugin/promhash-datasource/pkg/plugin/datasource.go`, `plugin/promhash-datasource/pkg/plugin/datasource_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import ( "context"; "net/http"; "net/http/httptest"; "testing"
    "github.com/grafana/grafana-plugin-sdk-go/backend" )

func TestCheckHealthOK(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`["payments"]`)) }))
    defer srv.Close()
    ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
    res, err := ds.CheckHealth(context.Background(), &backend.CheckHealthRequest{})
    if err != nil { t.Fatal(err) }
    if res.Status != backend.HealthStatusOk { t.Fatalf("status %v", res.Status) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -run TestCheckHealth -v ; cd -`
Expected: FAIL — `undefined: Datasource`.

- [ ] **Step 3: Implement Datasource + CheckHealth**

```go
package plugin

import (
    "context"; "net/http"; "time"
    "github.com/grafana/grafana-plugin-sdk-go/backend"
)

type Datasource struct{ apiURL string; hc *http.Client }

func NewDatasource(apiURL string) *Datasource {
    return &Datasource{apiURL: apiURL, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (d *Datasource) CheckHealth(ctx context.Context, _ *backend.CheckHealthRequest) (*backend.CheckHealthResult, error) {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+"/apps", nil)
    resp, err := d.hc.Do(req)
    if err != nil || resp.StatusCode != 200 {
        return &backend.CheckHealthResult{Status: backend.HealthStatusError, Message: "promhash API unreachable"}, nil
    }
    defer resp.Body.Close()
    return &backend.CheckHealthResult{Status: backend.HealthStatusOk, Message: "connected"}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -run TestCheckHealth -v ; cd -`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/promhash-datasource/pkg/
git commit -m "feat: plugin backend CheckHealth (C8)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 9.3: `QueryData` (app_path / impact → data frames)

**Files:**
- Create: `plugin/promhash-datasource/pkg/plugin/query.go`, `plugin/promhash-datasource/pkg/plugin/query_test.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import ( "context"; "encoding/json"; "net/http"; "net/http/httptest"; "testing"
    "github.com/grafana/grafana-plugin-sdk-go/backend" )

func TestQueryAppPath(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
        w.Write([]byte(`[{"device":"rtr-core-1","metricIfName":"Te0/1/2","ifIndex":42,"direction":"egress"}]`))
    }))
    defer srv.Close()
    ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
    raw, _ := json.Marshal(map[string]string{"queryType": "app_path", "app": "payments"})
    resp, err := ds.QueryData(context.Background(), &backend.QueryDataRequest{
        Queries: []backend.DataQuery{{RefID: "A", JSON: raw}}})
    if err != nil { t.Fatal(err) }
    dr := resp.Responses["A"]
    if dr.Error != nil { t.Fatal(dr.Error) }
    if len(dr.Frames) != 1 || dr.Frames[0].Rows() != 1 { t.Fatalf("frames %+v", dr.Frames) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -run TestQueryAppPath -v ; cd -`
Expected: FAIL — `undefined: (*Datasource).QueryData`.

- [ ] **Step 3: Implement QueryData**

```go
package plugin

import (
    "context"; "encoding/json"; "net/http"
    "github.com/grafana/grafana-plugin-sdk-go/backend"
    "github.com/grafana/grafana-plugin-sdk-go/data"
)

type query struct{ QueryType, App, Device, IfName string }
type hop struct {
    Device       string `json:"device"`
    IfName       string `json:"ifName"`
    MetricIfName string `json:"metricIfName"`
    Instance     string `json:"instance"`
    Direction    string `json:"direction"`
    IfIndex      int    `json:"ifIndex"`
}

func (d *Datasource) QueryData(ctx context.Context, req *backend.QueryDataRequest) (*backend.QueryDataResponse, error) {
    resp := backend.NewQueryDataResponse()
    for _, q := range req.Queries {
        var qm query
        if err := json.Unmarshal(q.JSON, &qm); err != nil { resp.Responses[q.RefID] = backend.ErrDataResponse(backend.StatusBadRequest, err.Error()); continue }
        frame, err := d.runQuery(ctx, qm)
        if err != nil { resp.Responses[q.RefID] = backend.ErrDataResponse(backend.StatusInternal, err.Error()); continue }
        resp.Responses[q.RefID] = backend.DataResponse{Frames: data.Frames{frame}}
    }
    return resp, nil
}

func (d *Datasource) runQuery(ctx context.Context, q query) (*data.Frame, error) {
    switch q.QueryType {
    case "app_path":
        var hops []hop
        if err := d.getJSON(ctx, "/apps/"+q.App+"/path", &hops); err != nil { return nil, err }
        dev := make([]string, len(hops)); iface := make([]string, len(hops)); idx := make([]int64, len(hops)); dir := make([]string, len(hops))
        for i, h := range hops { dev[i] = h.Device; iface[i] = h.MetricIfName; idx[i] = int64(h.IfIndex); dir[i] = h.Direction }
        return data.NewFrame("app_path",
            data.NewField("device", nil, dev), data.NewField("ifName", nil, iface),
            data.NewField("ifIndex", nil, idx), data.NewField("direction", nil, dir)), nil
    default: // impact / interface_apps
        var rows []map[string]string
        if err := d.getJSON(ctx, "/interfaces/"+q.Device+"/"+q.IfName+"/apps", &rows); err != nil { return nil, err }
        app := make([]string, len(rows)); svc := make([]string, len(rows)); cust := make([]string, len(rows))
        for i, r := range rows { app[i] = r["app"]; svc[i] = r["service"]; cust[i] = r["customer"] }
        return data.NewFrame("impact",
            data.NewField("app", nil, app), data.NewField("service", nil, svc), data.NewField("customer", nil, cust)), nil
    }
}

func (d *Datasource) getJSON(ctx context.Context, path string, out any) error {
    req, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+path, nil)
    resp, err := d.hc.Do(req); if err != nil { return err }
    defer resp.Body.Close()
    return json.NewDecoder(resp.Body).Decode(out)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -run TestQueryAppPath -v ; cd -`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add plugin/promhash-datasource/pkg/
git commit -m "feat: plugin QueryData for app_path and impact (C8)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 9.4: `CallResource` (variable queries) + plugin entrypoint

**Files:**
- Create: `plugin/promhash-datasource/pkg/plugin/resource.go`, `plugin/promhash-datasource/pkg/plugin/resource_test.go`
- Create: `plugin/promhash-datasource/pkg/main.go`

- [ ] **Step 1: Write the failing test**

```go
package plugin

import ( "context"; "encoding/json"; "net/http"; "net/http/httptest"; "testing"
    "github.com/grafana/grafana-plugin-sdk-go/backend" )

type capSender struct{ body []byte }
func (c *capSender) Send(r *backend.CallResourceResponse) error { c.body = r.Body; return nil }

func TestCallResourceApps(t *testing.T) {
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`["payments","ledger"]`)) }))
    defer srv.Close()
    ds := &Datasource{apiURL: srv.URL, hc: srv.Client()}
    cs := &capSender{}
    err := ds.CallResource(context.Background(), &backend.CallResourceRequest{Path: "apps"}, cs)
    if err != nil { t.Fatal(err) }
    var out []string
    _ = json.Unmarshal(cs.body, &out)
    if len(out) != 2 { t.Fatalf("got %v", out) }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -run TestCallResource -v ; cd -`
Expected: FAIL — `undefined: (*Datasource).CallResource`.

- [ ] **Step 3: Implement CallResource + main**

```go
// resource.go
package plugin

import (
    "context"; "io"; "net/http"; "strings"
    "github.com/grafana/grafana-plugin-sdk-go/backend"
)

// CallResource serves variable queries: "apps" and "path_interfaces/<app>".
func (d *Datasource) CallResource(ctx context.Context, req *backend.CallResourceRequest, sender backend.CallResourceResponseSender) error {
    var upstream string
    switch {
    case req.Path == "apps":
        upstream = "/apps"
    case strings.HasPrefix(req.Path, "path_interfaces/"):
        upstream = "/apps/" + strings.TrimPrefix(req.Path, "path_interfaces/") + "/path"
    default:
        return sender.Send(&backend.CallResourceResponse{Status: http.StatusNotFound})
    }
    r, _ := http.NewRequestWithContext(ctx, http.MethodGet, d.apiURL+upstream, nil)
    resp, err := d.hc.Do(r); if err != nil { return err }
    defer resp.Body.Close()
    body, _ := io.ReadAll(resp.Body)
    return sender.Send(&backend.CallResourceResponse{Status: http.StatusOK, Body: body})
}
```

```go
// pkg/main.go
package main

import (
    "context"; "encoding/json"; "os"
    "github.com/grafana/grafana-plugin-sdk-go/backend"
    "github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
    "github.com/grafana/grafana-plugin-sdk-go/backend/instancemgmt"
    "github.com/grafana/grafana-plugin-sdk-go/backend/log"
    "github.com/starkweb/promhash-datasource/pkg/plugin"
)

func main() {
    if err := datasource.Manage("starkweb-promhash-datasource", newInstance, datasource.ManageOpts{}); err != nil {
        log.DefaultLogger.Error(err.Error()); os.Exit(1)
    }
}

func newInstance(_ context.Context, s backend.DataSourceInstanceSettings) (instancemgmt.Instance, error) {
    var cfg struct{ APIURL string `json:"apiUrl"` }
    _ = json.Unmarshal(s.JSONData, &cfg)
    return plugin.NewDatasource(cfg.APIURL), nil
}
```

`Datasource` now satisfies `backend.CheckHealthHandler` (Task 9.2), `backend.QueryDataHandler`
(Task 9.3), and `backend.CallResourceHandler` (this task), so `datasource.Manage` wires all three.

- [ ] **Step 4: Run tests + build the plugin binary**

Run: `cd plugin/promhash-datasource && go test ./pkg/plugin/ -v && go build -o gpx_promhash ./pkg && rm -f gpx_promhash ; cd -`
Expected: PASS, build OK.

- [ ] **Step 5: Commit**

```bash
git add plugin/promhash-datasource/pkg/
git commit -m "feat: plugin CallResource variable queries + entrypoint (C8)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

### Task 9.5: Frontend query editor + config (smoke)

**Files:**
- Create: `plugin/promhash-datasource/src/datasource.ts`, `src/ConfigEditor.tsx`, `src/QueryEditor.tsx`, `src/module.ts`, `src/types.ts`

- [ ] **Step 1: Implement the minimal frontend**

`src/types.ts`:
```ts
import { DataSourceJsonData } from '@grafana/data';
import { DataQuery } from '@grafana/schema';
export interface PromhashQuery extends DataQuery { queryType: 'app_path' | 'impact'; app?: string; device?: string; ifName?: string; }
export interface PromhashOptions extends DataSourceJsonData { apiUrl?: string; }
```

`src/datasource.ts`:
```ts
import { DataSourceWithBackend } from '@grafana/runtime';
import { DataSourceInstanceSettings } from '@grafana/data';
import { PromhashQuery, PromhashOptions } from './types';
export class DataSource extends DataSourceWithBackend<PromhashQuery, PromhashOptions> {
  constructor(s: DataSourceInstanceSettings<PromhashOptions>) { super(s); }
  async apps(): Promise<string[]> { return this.getResource('apps'); }
}
```

`src/QueryEditor.tsx`:
```tsx
import React from 'react';
import { QueryEditorProps } from '@grafana/data';
import { Input, Select } from '@grafana/ui';
import { DataSource } from './datasource';
import { PromhashQuery, PromhashOptions } from './types';
export function QueryEditor(props: QueryEditorProps<DataSource, PromhashQuery, PromhashOptions>) {
  const { query, onChange } = props;
  return (
    <div>
      <Select width={20} options={[{label:'App path',value:'app_path'},{label:'Impact',value:'impact'}]}
              value={query.queryType} onChange={(v)=>onChange({...query, queryType: v.value as any})} />
      <Input placeholder="app" value={query.app ?? ''} onChange={(e)=>onChange({...query, app: e.currentTarget.value})} />
    </div>
  );
}
```

`src/ConfigEditor.tsx`:
```tsx
import React from 'react';
import { DataSourcePluginOptionsEditorProps } from '@grafana/data';
import { Input, InlineField } from '@grafana/ui';
import { PromhashOptions } from './types';
export function ConfigEditor(props: DataSourcePluginOptionsEditorProps<PromhashOptions>) {
  const { options, onOptionsChange } = props;
  return (
    <InlineField label="promhash API URL">
      <Input value={options.jsonData.apiUrl ?? ''}
             onChange={(e)=>onOptionsChange({...options, jsonData: {...options.jsonData, apiUrl: e.currentTarget.value}})} />
    </InlineField>
  );
}
```

`src/module.ts`:
```ts
import { DataSourcePlugin } from '@grafana/data';
import { DataSource } from './datasource';
import { ConfigEditor } from './ConfigEditor';
import { QueryEditor } from './QueryEditor';
export const plugin = new DataSourcePlugin(DataSource).setConfigEditor(ConfigEditor).setQueryEditor(QueryEditor);
```

- [ ] **Step 2: Type-check (smoke)**

Run: `cd plugin/promhash-datasource && npm install && npx tsc --noEmit ; cd -`
Expected: no type errors (network access to npm required).

- [ ] **Step 3: Document signing + GitOps deploy**

Create `plugin/promhash-datasource/README.md`: build backend (`mage` or `go build -o dist/gpx_promhash ./pkg`), build frontend (`npm run build`), **privately sign** for Grafana Enterprise (or add the plugin id to `GF_PLUGINS_ALLOW_LOADING_UNSIGNED_PLUGINS`), and deploy `dist/` via the existing GitOps pipeline.

- [ ] **Step 4: Commit**

```bash
git add plugin/promhash-datasource/src/ plugin/promhash-datasource/README.md
git commit -m "feat: plugin frontend query/config editors + deploy docs (C8)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Final Verification

### Task 10.1: Full build + test sweep

- [ ] **Step 1: Unit tests + vet across the module**

Run: `make test && go vet ./...`
Expected: all PASS.

- [ ] **Step 2: Integration tests (Docker required)**

Run: `make test-int`
Expected: all PASS (Neo4j containers spin up per package).

- [ ] **Step 3: Build all binaries**

Run: `go build ./... && (cd plugin/promhash-datasource && go build ./pkg && rm -f gpx_promhash)`
Expected: exit 0.

- [ ] **Step 4: End-to-end smoke (manual, documented)**

Document in `docs/deploy/smoke.md`: start Neo4j + a stub Prometheus → `promhash-seed` → `promhash-catalog` → put `declared/payments.yaml` → `promhash-loader -validate-only` (passes) → `promhash-loader` → `promhash-enrich -apps payments` (writes `gitops/enrichment/payments/{federate.match,rules.yaml}`) → `promhash-api` → `curl /apps/payments/path` returns the hop set.

- [ ] **Step 5: Commit docs**

```bash
git add docs/deploy/smoke.md
git commit -m "docs: end-to-end smoke runbook

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## Notes on Execution Order

Task 4.3's integration test depends on `AppPath` (Task 5.1). When executing strictly top-to-bottom, implement Task 5.1's `AppPath` method **before** running Task 4.3's integration test (or run 4.3's test after Phase 5). All unit tests are independent and can run as written.

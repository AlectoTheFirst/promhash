package graph

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/phash"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// declaredConfidence is the confidence stamped on relationships created from a
// declaration. Declared topology is taken as ground truth (1.0); flow-derived
// overlays may later stamp lower values.
const declaredConfidence = 1.0

// Repo provides access to the topology graph stored in a single Neo4j
// database. All operations run against the database named at construction.
type Repo struct {
	drv neo4j.DriverWithContext
	db  string
}

// New returns a Repo that issues queries through drv against the database db.
func New(drv neo4j.DriverWithContext, db string) *Repo { return &Repo{drv: drv, db: db} }

func (r *Repo) write(ctx context.Context, cy string, params map[string]any) error {
	_, err := neo4j.ExecuteQuery(ctx, r.drv, cy, params,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	return err
}

// execWrite opens a managed write session and runs fn inside a single
// ExecuteWrite transaction. The driver retries fn on transient errors;
// fn must be idempotent. The session is always closed before returning.
func (r *Repo) execWrite(ctx context.Context, fn func(tx neo4j.ManagedTransaction) error) error {
	sess := r.drv.NewSession(ctx, neo4j.SessionConfig{DatabaseName: r.db, AccessMode: neo4j.AccessModeWrite})
	defer sess.Close(ctx)
	_, err := sess.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		return nil, fn(tx)
	})
	return err
}

// Ping verifies the database is reachable by running a trivial read query.
// It is used by readiness probes.
func (r *Repo) Ping(ctx context.Context) error {
	_, err := neo4j.ExecuteQuery(ctx, r.drv, "RETURN 1", nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	return err
}

// EnsureConstraints creates the uniqueness constraints on the "phash" property
// for every node label used by the graph, if they do not already exist. It is
// idempotent and safe to call on every startup.
func (r *Repo) EnsureConstraints(ctx context.Context) error {
	for _, label := range []string{LabelInterface, LabelDevice, "Application", "ApplicationService",
		"BusinessService", "Customer", "Endpoint", "IP", "Segment", "Connection", "Path"} {
		cy := fmt.Sprintf(
			"CREATE CONSTRAINT phash_%s IF NOT EXISTS FOR (n:%s) REQUIRE n.phash IS UNIQUE", label, label)
		if err := r.write(ctx, cy, nil); err != nil {
			return err
		}
	}
	if err := r.write(ctx, `CREATE CONSTRAINT app_name_unique IF NOT EXISTS FOR (a:Application) REQUIRE a.name IS UNIQUE`, nil); err != nil {
		return err
	}
	return nil
}

// UpsertInterface creates or updates the Interface node keyed by i.PHash,
// overwriting its properties with the values in i and marking its provenance
// as "observed".
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

// GetInterfaceByPHash returns the Interface identified by phash. It returns
// ErrNotFound if no such interface exists.
func (r *Repo) GetInterfaceByPHash(ctx context.Context, phash string) (Iface, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (n:Interface {phash:$phash}) RETURN n`, map[string]any{"phash": phash},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return Iface{}, err
	}
	if len(res.Records) == 0 {
		return Iface{}, ErrNotFound
	}
	n, _ := res.Records[0].Get("n")
	props := n.(neo4j.Node).Props
	return ifaceFromProps(props), nil
}

// ListAllInterfaces returns every Interface node in the graph.
func (r *Repo) ListAllInterfaces(ctx context.Context) ([]Iface, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv, `MATCH (n:Interface) RETURN n`, nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(res.Records))
	for _, rec := range res.Records {
		n, _ := rec.Get("n")
		out = append(out, ifaceFromProps(n.(neo4j.Node).Props))
	}
	return out, nil
}

// ErrNotFound is returned by lookups when no matching node exists in the graph.
var ErrNotFound = fmt.Errorf("graph: node not found")

// ErrAmbiguousInterface is returned when more than one Interface node shares the
// same (instance, ifIndex). The catalog should make this pair unique; if it ever
// occurs the caller treats it as no match rather than guessing.
var ErrAmbiguousInterface = fmt.Errorf("graph: multiple interfaces match instance+ifIndex")

func ifaceFromProps(p map[string]any) Iface {
	gs := func(k string) string {
		if v, ok := p[k].(string); ok {
			return v
		}
		return ""
	}
	gi := func(k string) int {
		if v, ok := p[k].(int64); ok {
			return int(v)
		}
		return 0
	}
	out := Iface{PHash: gs("phash"), Device: gs("device"), IfName: gs("ifName"),
		MetricIfName: gs("metricIfName"), IfDescr: gs("ifDescr"), IfAlias: gs("ifAlias"),
		Instance: gs("instance"), Vendor: gs("vendor"), IfIndex: gi("ifIndex")}
	if v, ok := p["observedAt"].(int64); ok {
		out.ObservedAt = time.Unix(v, 0).UTC()
	}
	return out
}

// DeclaredHop is a resolved hop ready to persist.
type DeclaredHop struct {
	IfacePHash string
	Seq        int
	Direction  string
}

// DeclaredPath is an ordered sequence of hops describing one route a
// dependency takes through the network.
type DeclaredPath struct{ Hops []DeclaredHop }

// DeclaredDep is a declared dependency on another application service.
// ToAppSvc is the target service's phash and ToName its display name; Paths
// holds the one or more routes the dependency may follow.
type DeclaredDep struct {
	ToAppSvc, ToName string
	Paths            []DeclaredPath
}

// DeclaredApp is a complete declared application topology to persist: the
// application and its service (by phash and name), its owner, criticality and
// consuming customers, and its declared dependencies. Source records the
// origin of the declaration and ValidFrom the time from which it becomes
// effective. Criticality is free-form business metadata surfaced by impact
// queries (empty when undeclared).
type DeclaredApp struct {
	AppPHash, App, AppSvcPHash, AppSvc, Owner, Criticality string
	Customers                                              []string
	Deps                                                   []DeclaredDep
	Source                                                 string
	ValidFrom                                              time.Time
}

// upsertDeclaredAppTx runs the UpsertDeclaredApp Cypher inside the caller's
// managed transaction tx.
//
// DEPENDS_ON and TAKES are append-only: {provenance,source,validFrom} sit in the
// MERGE pattern, so a re-declaration at a new validFrom creates a fresh edge
// rather than re-matching a closed one. The SET ...validTo=null is safe because a
// closed edge always carries an earlier validFrom and is never re-matched here;
// it only keeps an idempotent same-validFrom reload open. This invariant relies on
// validFrom being strictly increasing across reloads (see plan task D2).
func upsertDeclaredAppTx(ctx context.Context, tx neo4j.ManagedTransaction, d DeclaredApp) error {
	res, err := tx.Run(ctx,
		`MERGE (app:Application {phash:$appPHash}) SET app.name=$app, app.owner=$owner, app.criticality=$criticality
         MERGE (svc:ApplicationService {phash:$svcPHash}) SET svc.name=$appSvc
         MERGE (app)-[:RUNS_AS]->(svc)
         // FOREACH does not collapse cardinality on an empty list, so customer
         // creation never gates the dependency creation below.
         FOREACH (cust IN $customers |
           MERGE (c:Customer {phash:cust.phash}) SET c.name=cust.name
           MERGE (bs:BusinessService {phash:cust.bsPHash}) SET bs.name=$app
           MERGE (c)-[:CONSUMES]->(bs) MERGE (bs)-[:REALIZED_BY]->(svc))
         WITH svc
         UNWIND $deps AS dep
           MERGE (target:ApplicationService {phash:dep.toPHash}) SET target.name=dep.to
           MERGE (svc)-[do:DEPENDS_ON {provenance:'declared', source:$source, validFrom:$validFrom}]->(target)
             SET do.confidence=$confidence, do.observedAt=$observedAt, do.validTo=null
           MERGE (conn:Connection {phash:dep.connPHash})
             SET conn.provenance='declared', conn.source=$source, conn.confidence=$confidence,
                 conn.observedAt=$observedAt, conn.validFrom=$validFrom, conn.validTo=null
           MERGE (svc)-[:USES]->(conn) MERGE (conn)-[:TO_SVC]->(target)
           WITH conn, dep
           UNWIND dep.paths AS p
             MERGE (path:Path {phash:p.pathPHash})
               SET path.provenance='declared', path.source=$source
             MERGE (conn)-[tk:TAKES {provenance:'declared', source:$source, validFrom:$validFrom}]->(path)
               SET tk.validTo=null, tk.confidence=$confidence, tk.observedAt=$observedAt
             WITH path, p
             UNWIND p.hops AS h
               MATCH (iface:Interface {phash:h.ifacePHash})
               MERGE (path)-[:HOP {seq:h.seq, direction:h.direction}]->(iface)`,
		map[string]any{
			"appPHash": d.AppPHash, "app": d.App, "owner": d.Owner, "criticality": d.Criticality,
			"svcPHash": d.AppSvcPHash, "appSvc": d.AppSvc,
			"customers": customersToParams(d.Customers, d.App),
			"source":    d.Source, "validFrom": d.ValidFrom.Unix(),
			"confidence": declaredConfidence, "observedAt": d.ValidFrom.Unix(),
			"deps": depsToParams(d.AppSvcPHash, d.Deps, d.Source, d.ValidFrom),
		})
	if err != nil {
		return err
	}
	_, err = res.Consume(ctx)
	return err
}

// UpsertDeclaredApp persists a declared application topology: it merges the
// Application and its ApplicationService, links consuming customers via
// BusinessService nodes, and creates the declared dependencies together with
// their Connection, Path and HOP relationships. The created dependency,
// connection and path edges are stamped with provenance "declared", the given
// source and d.ValidFrom, and are left open (validTo null). Interfaces
// referenced by hops must already exist.
func (r *Repo) UpsertDeclaredApp(ctx context.Context, d DeclaredApp) error {
	return r.execWrite(ctx, func(tx neo4j.ManagedTransaction) error {
		return upsertDeclaredAppTx(ctx, tx, d)
	})
}

// customersToParams resolves each customer name to its Customer phash and the
// phash of the BusinessService realizing app for that customer, computed in Go
// via phash.Hash so identity matches the rest of the system and avoids the
// collision-prone string concatenation that hand-built ids used.
func customersToParams(customers []string, app string) []map[string]any {
	out := make([]map[string]any, 0, len(customers))
	for _, cust := range customers {
		out = append(out, map[string]any{
			"phash":   phash.Hash(phash.KindCustomer, cust),
			"name":    cust,
			"bsPHash": phash.Hash(phash.KindBizSvc, cust, app),
		})
	}
	return out
}

// depsToParams converts declared dependencies into Cypher parameter maps.
// Each dep carries a connPHash derived from (svcPHash, targetPHash, source,
// validFrom) so that Connection nodes are stable across ExecuteWrite retries
// and identical-params replays. Each path carries a pathPHash derived from
// (connPHash, pathIndex) for the same reason. Both use phash.Hash so the
// identity scheme is consistent with the rest of the system.
func depsToParams(svcPHash string, deps []DeclaredDep, source string, validFrom time.Time) []map[string]any {
	vfStr := strconv.FormatInt(validFrom.Unix(), 10)
	out := make([]map[string]any, 0, len(deps))
	for _, dep := range deps {
		connPHash := phash.Hash(phash.KindConnection, svcPHash, dep.ToAppSvc, source, vfStr)
		paths := make([]map[string]any, 0, len(dep.Paths))
		for i, p := range dep.Paths {
			pathPHash := phash.Hash(phash.KindPath, connPHash, strconv.Itoa(i))
			hops := make([]map[string]any, 0, len(p.Hops))
			for _, h := range p.Hops {
				hops = append(hops, map[string]any{"ifacePHash": h.IfacePHash, "seq": h.Seq, "direction": h.Direction})
			}
			paths = append(paths, map[string]any{"pathPHash": pathPHash, "hops": hops})
		}
		out = append(out, map[string]any{"toPHash": dep.ToAppSvc, "to": dep.ToName, "connPHash": connPHash, "paths": paths})
	}
	return out
}

// closeAppValidityTx runs the three CloseAppValidity Cypher statements inside
// the caller's managed transaction tx.
//
// Each close re-MATCHes from the application independently so that the
// absence of an open TAKES never zeroes out the rows for the DEPENDS_ON or
// Connection closes. Reusing a chained WITH would drop cardinality to zero
// whenever a dependency has no open TAKES, leaving stale-open edges that a
// reload could no longer supersede.
func closeAppValidityTx(ctx context.Context, tx neo4j.ManagedTransaction, appPHash string, at time.Time) error {
	res, err := tx.Run(ctx,
		`MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(conn:Connection)-[t:TAKES]->(:Path)
         WHERE t.validTo IS NULL SET t.validTo=$at, conn.validTo=$at`,
		map[string]any{"appPHash": appPHash, "at": at.Unix()})
	if err != nil {
		return err
	}
	if _, err = res.Consume(ctx); err != nil {
		return err
	}
	res, err = tx.Run(ctx,
		`MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(conn:Connection)
         WHERE conn.validTo IS NULL SET conn.validTo=$at`,
		map[string]any{"appPHash": appPHash, "at": at.Unix()})
	if err != nil {
		return err
	}
	if _, err = res.Consume(ctx); err != nil {
		return err
	}
	res, err = tx.Run(ctx,
		`MATCH (:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[do:DEPENDS_ON]->()
         WHERE do.validTo IS NULL SET do.validTo=$at`,
		map[string]any{"appPHash": appPHash, "at": at.Unix()})
	if err != nil {
		return err
	}
	_, err = res.Consume(ctx)
	return err
}

// CloseAppValidity ends the currently open declarations for the application
// identified by appPHash by setting validTo to at on its open DEPENDS_ON and
// TAKES relationships and their connections. It is typically called before
// upserting a new revision so that history is preserved.
func (r *Repo) CloseAppValidity(ctx context.Context, appPHash string, at time.Time) error {
	return r.execWrite(ctx, func(tx neo4j.ManagedTransaction) error {
		return closeAppValidityTx(ctx, tx, appPHash, at)
	})
}

// ReloadDeclaredApp closes the currently open declarations for d.AppPHash (as of
// at) and upserts the new revision atomically in a single managed write
// transaction. A crash or error after the closes but before the upsert is fully
// written causes the entire transaction to roll back, so the app never vanishes
// from current state.
//
// To prevent zero-width validity windows ([T,T) which no point-in-time query
// can satisfy), the effective timestamp is bumped to prevValidFrom+1s when the
// requested at falls within the same second as the app's current open
// validFrom. Both the close and the new upsert use this effective time so the
// timeline stays contiguous (old.validTo == new.validFrom) and strictly
// increasing. The read of the current max validFrom happens inside the same
// managed transaction as the writes, so the check-then-act is atomic.
func (r *Repo) ReloadDeclaredApp(ctx context.Context, d DeclaredApp, at time.Time) error {
	return r.execWrite(ctx, func(tx neo4j.ManagedTransaction) error {
		// Read the app's current maximum open validFrom within this transaction
		// so the monotonicity check and the writes are one atomic operation.
		res, err := tx.Run(ctx,
			`MATCH (app:Application {phash:$p})-[:RUNS_AS]->(:ApplicationService)-[do:DEPENDS_ON]->()
			 WHERE do.validTo IS NULL
			 RETURN max(do.validFrom) AS prev`,
			map[string]any{"p": d.AppPHash})
		if err != nil {
			return err
		}
		rec, err := res.Single(ctx)
		if err != nil {
			return err
		}

		effUnix := at.Unix()
		if prev, ok := rec.Get("prev"); ok && prev != nil {
			if prevUnix, ok := prev.(int64); ok && effUnix <= prevUnix {
				effUnix = prevUnix + 1
			}
		}
		effTime := time.Unix(effUnix, 0).UTC()

		if err := closeAppValidityTx(ctx, tx, d.AppPHash, effTime); err != nil {
			return err
		}
		// Use a local copy so we do not mutate the caller's struct.
		dLocal := d
		dLocal.ValidFrom = effTime
		return upsertDeclaredAppTx(ctx, tx, dLocal)
	})
}

// AppPath returns the ordered hops of the application's path that were valid at
// time at, considering only TAKES relationships whose validity window contains
// at. Hops are returned sorted by sequence.
func (r *Repo) AppPath(ctx context.Context, appPHash string, at time.Time) ([]Hop, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (a:Application {phash:$app})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(c:Connection)-[t:TAKES]->(p:Path)-[h:HOP]->(i:Interface)
         WHERE ($at >= t.validFrom) AND (t.validTo IS NULL OR $at < t.validTo)
         RETURN DISTINCT i.device AS device, i.ifName AS ifName, i.metricIfName AS metricIfName,
                i.instance AS instance, i.ifIndex AS ifIndex, h.seq AS seq, h.direction AS direction,
                t.provenance AS provenance, t.confidence AS confidence
         ORDER BY seq`,
		map[string]any{"app": appPHash, "at": at.Unix()},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	out := make([]Hop, 0, len(res.Records))
	for _, rec := range res.Records {
		gs := func(k string) string { v, _ := rec.Get(k); s, _ := v.(string); return s }
		gi := func(k string) int { v, _ := rec.Get(k); n, _ := v.(int64); return int(n) }
		gf := func(k string) float64 { v, _ := rec.Get(k); f, _ := v.(float64); return f }
		prov := gs("provenance")
		if prov == "" {
			prov = "declared"
		}
		out = append(out, Hop{Device: gs("device"), IfName: gs("ifName"), MetricIfName: gs("metricIfName"),
			Instance: gs("instance"), IfIndex: gi("ifIndex"), Seq: gi("seq"), Direction: gs("direction"),
			Provenance: prov, Confidence: gf("confidence")})
	}
	return out, nil
}

// InterfaceImpact returns the applications and services affected by the
// interface identified by ifacePHash at time at, traversing paths whose
// validity window contains at. Each row includes any consuming customer and
// owner/criticality metadata, with one row per distinct app/service/customer
// combination.
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
	if err != nil {
		return nil, err
	}
	out := make([]ImpactRow, 0, len(res.Records))
	for _, rec := range res.Records {
		gs := func(k string) string { v, _ := rec.Get(k); s, _ := v.(string); return s }
		out = append(out, ImpactRow{App: gs("app"), Service: gs("service"), Owner: gs("owner"),
			Customer: gs("customer"), Criticality: gs("criticality")})
	}
	return out, nil
}

// InterfaceImpactByInstanceIndex resolves the Interface by exact (instance,
// ifIndex) and returns its impact rows at time at, reusing the InterfaceImpact
// traversal so impact logic lives in one place. Zero matches → (nil, nil): the
// interface is simply not in the graph, which callers treat as "no impact".
// More than one match → ErrAmbiguousInterface (should not happen).
func (r *Repo) InterfaceImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]ImpactRow, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (n:Interface {instance:$instance, ifIndex:$ifIndex}) RETURN n.phash AS phash`,
		map[string]any{"instance": instance, "ifIndex": ifIndex},
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	switch len(res.Records) {
	case 0:
		return nil, nil
	case 1:
		v, _ := res.Records[0].Get("phash")
		p, _ := v.(string)
		return r.InterfaceImpact(ctx, p, at)
	default:
		return nil, ErrAmbiguousInterface
	}
}

// ListApps returns the names of all Application nodes, sorted alphabetically.
func (r *Repo) ListApps(ctx context.Context) ([]string, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (a:Application) RETURN a.name AS n ORDER BY n`, nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Records))
	for _, rec := range res.Records {
		v, _ := rec.Get("n")
		s, _ := v.(string)
		out = append(out, s)
	}
	return out, nil
}

// UpsertAppSeed creates or updates the minimal Application and
// ApplicationService nodes (and the RUNS_AS relationship between them) from
// seed data, recording the external system identifier sysID on the
// application. It establishes a stub that later declarations can enrich.
func (r *Repo) UpsertAppSeed(ctx context.Context, appPHash, app, svcPHash, svc, sysID string) error {
	return r.write(ctx,
		`MERGE (a:Application {phash:$appPHash}) SET a.name=$app, a.sysId=$sysID
         MERGE (s:ApplicationService {phash:$svcPHash}) SET s.name=$svc
         MERGE (a)-[:RUNS_AS]->(s)`,
		map[string]any{"appPHash": appPHash, "app": app, "svcPHash": svcPHash, "svc": svc, "sysID": sysID})
}

// ListOpenDeclaredApps returns the distinct app phashes for every Application
// that currently has at least one open (validTo IS NULL) declared DEPENDS_ON
// edge. It is used by the loader's reconcile pass to detect apps whose
// declaration YAML was deleted and must be tombstoned.
//
// NOTE: Do NOT use ListApps for this purpose — it returns every Application
// node including seed-only stubs that have no declared edges.
func (r *Repo) ListOpenDeclaredApps(ctx context.Context) ([]string, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (app:Application)-[:RUNS_AS]->(:ApplicationService)-[do:DEPENDS_ON]->()
		 WHERE do.validTo IS NULL AND do.provenance='declared'
		 RETURN DISTINCT app.phash AS p`,
		nil,
		neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(res.Records))
	for _, rec := range res.Records {
		v, _ := rec.Get("p")
		s, _ := v.(string)
		out = append(out, s)
	}
	return out, nil
}

// AppServiceName returns the name of the ApplicationService that the named
// application runs as. If the application has no service or the lookup fails,
// it falls back to returning app unchanged.
func (r *Repo) AppServiceName(ctx context.Context, app string) (string, error) {
	res, err := neo4j.ExecuteQuery(ctx, r.drv,
		`MATCH (:Application {name:$app})-[:RUNS_AS]->(s:ApplicationService) RETURN s.name AS n LIMIT 1`,
		map[string]any{"app": app}, neo4j.EagerResultTransformer, neo4j.ExecuteQueryWithDatabase(r.db))
	if err != nil || len(res.Records) == 0 {
		return app, err
	}
	v, _ := res.Records[0].Get("n")
	s, _ := v.(string)
	return s, nil
}

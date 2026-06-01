package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

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

// EnsureConstraints creates the uniqueness constraints on the "phash" property
// for every node label used by the graph, if they do not already exist. It is
// idempotent and safe to call on every startup.
func (r *Repo) EnsureConstraints(ctx context.Context) error {
	for _, label := range []string{LabelInterface, LabelDevice, "Application", "ApplicationService",
		"BusinessService", "Customer", "Endpoint", "IP", "Segment"} {
		cy := fmt.Sprintf(
			"CREATE CONSTRAINT phash_%s IF NOT EXISTS FOR (n:%s) REQUIRE n.phash IS UNIQUE", label, label)
		if err := r.write(ctx, cy, nil); err != nil {
			return err
		}
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
	return Iface{PHash: gs("phash"), Device: gs("device"), IfName: gs("ifName"),
		MetricIfName: gs("metricIfName"), IfDescr: gs("ifDescr"), IfAlias: gs("ifAlias"),
		Instance: gs("instance"), Vendor: gs("vendor"), IfIndex: gi("ifIndex")}
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
// application and its service (by phash and name), its owner and consuming
// customers, and its declared dependencies. Source records the origin of the
// declaration and ValidFrom the time from which it becomes effective.
type DeclaredApp struct {
	AppPHash, App, AppSvcPHash, AppSvc, Owner string
	Customers                                 []string
	Deps                                      []DeclaredDep
	Source                                    string
	ValidFrom                                 time.Time
}

// UpsertDeclaredApp persists a declared application topology: it merges the
// Application and its ApplicationService, links consuming customers via
// BusinessService nodes, and creates the declared dependencies together with
// their Connection, Path and HOP relationships. The created dependency,
// connection and path edges are stamped with provenance "declared", the given
// source and d.ValidFrom, and are left open (validTo null). Interfaces
// referenced by hops must already exist.
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
             MERGE (conn)-[tk:TAKES {provenance:'declared', source:$source, validFrom:$validFrom}]->(path)
               SET tk.validTo=null
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

// CloseAppValidity ends the currently open declarations for the application
// identified by appPHash by setting validTo to at on its open DEPENDS_ON and
// TAKES relationships and their connections. It is typically called before
// upserting a new revision so that history is preserved.
func (r *Repo) CloseAppValidity(ctx context.Context, appPHash string, at time.Time) error {
	return r.write(ctx,
		`MATCH (app:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(conn:Connection)-[t:TAKES]->(:Path)
         WHERE t.validTo IS NULL SET t.validTo=$at, conn.validTo=$at
         WITH svc
         MATCH (svc)-[do:DEPENDS_ON]->() WHERE do.validTo IS NULL SET do.validTo=$at`,
		map[string]any{"appPHash": appPHash, "at": at.Unix()})
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
                i.instance AS instance, i.ifIndex AS ifIndex, h.seq AS seq, h.direction AS direction
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
		out = append(out, Hop{Device: gs("device"), IfName: gs("ifName"), MetricIfName: gs("metricIfName"),
			Instance: gs("instance"), IfIndex: gi("ifIndex"), Seq: gi("seq"), Direction: gs("direction"),
			Provenance: "declared"})
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

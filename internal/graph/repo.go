package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type Repo struct {
	drv neo4j.DriverWithContext
	db  string
}

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
		if err := r.write(ctx, cy, nil); err != nil {
			return err
		}
	}
	return nil
}

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
type DeclaredPath struct{ Hops []DeclaredHop }
type DeclaredDep struct {
	ToAppSvc, ToName string
	Paths            []DeclaredPath
}
type DeclaredApp struct {
	AppPHash, App, AppSvcPHash, AppSvc, Owner string
	Customers                                 []string
	Deps                                      []DeclaredDep
	Source                                    string
	ValidFrom                                 time.Time
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

func (r *Repo) CloseAppValidity(ctx context.Context, appPHash string, at time.Time) error {
	return r.write(ctx,
		`MATCH (app:Application {phash:$appPHash})-[:RUNS_AS]->(svc:ApplicationService)
         MATCH (svc)-[:USES]->(conn:Connection)-[t:TAKES]->(:Path)
         WHERE t.validTo IS NULL SET t.validTo=$at, conn.validTo=$at
         WITH svc
         MATCH (svc)-[do:DEPENDS_ON]->() WHERE do.validTo IS NULL SET do.validTo=$at`,
		map[string]any{"appPHash": appPHash, "at": at.Unix()})
}

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

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

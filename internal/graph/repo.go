package graph

import (
	"context"
	"fmt"

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

package declare

import (
	"context"
	"fmt"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/catalog"
	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
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
				if err != nil {
					return fmt.Errorf("load %s: %w", a.App, err)
				}
				gp.Hops = append(gp.Hops, graph.DeclaredHop{IfacePHash: ifc.PHash, Seq: seq + 1, Direction: h.Direction})
			}
			gd.Paths = append(gd.Paths, gp)
		}
		da.Deps = append(da.Deps, gd)
	}
	// Supersede any currently-open edges so re-declaration replaces rather than
	// duplicates (Connection/Path are CREATEd). First load is a no-op.
	if err := r.CloseAppValidity(ctx, da.AppPHash, validFrom); err != nil {
		return err
	}
	return r.UpsertDeclaredApp(ctx, da)
}

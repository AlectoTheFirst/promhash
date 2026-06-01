package declare

import (
	"fmt"

	"github.com/starkweb/promhash/internal/catalog"
)

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

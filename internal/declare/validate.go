package declare

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/AlectoTheFirst/promhash/internal/catalog"
)

// validDirections is the closed set of traffic directions a hop may declare.
// It mirrors the RuleGroup mapping in internal/enrich: egress -> ifHCOutOctets,
// ingress -> ifHCInOctets, transit -> both. Any other value is rejected so the
// CI gate fails before a malformed direction reaches rule generation.
var validDirections = map[string]bool{"ingress": true, "egress": true, "transit": true}

// Validate checks a declaration before it is loaded. It (a) requires the
// identity fields App and RunsAs to be present and free of characters that
// would corrupt identity keys, (b) rejects customer and dependency target
// names containing those same characters, (c) checks every hop's Direction
// against the closed direction set, and (d) resolves every hop's (device, if)
// against the catalog. Returns one error per problem — the CI gate fails if the
// slice is non-empty.
func Validate(a App, r *catalog.Resolver) []error {
	var errs []error

	// Identity fields must be present and key-safe. The ':' separator and
	// control characters would corrupt the identity keys derived from these
	// values, so they are rejected outright.
	if strings.TrimSpace(a.App) == "" {
		errs = append(errs, fmt.Errorf("app: required field is empty"))
	}
	if strings.TrimSpace(a.RunsAs) == "" {
		errs = append(errs, fmt.Errorf("app %q: runs_as is required and empty", a.App))
	}
	if err := keySafe("app", a.App); err != nil {
		errs = append(errs, err)
	}
	if err := keySafe("runs_as", a.RunsAs); err != nil {
		errs = append(errs, err)
	}
	for _, cust := range a.ConsumedByCustomers {
		if err := keySafe("consumed_by_customers", cust); err != nil {
			errs = append(errs, fmt.Errorf("app %q: %w", a.App, err))
		}
	}

	for _, dep := range a.DependsOn {
		if strings.TrimSpace(dep.To) == "" {
			errs = append(errs, fmt.Errorf("app %q dep: 'to' is required and empty", a.App))
		}
		if err := keySafe("dep.to", dep.To); err != nil {
			errs = append(errs, fmt.Errorf("app %q: %w", a.App, err))
		}
		candidates := dep.Candidates()
		if len(candidates) == 0 {
			errs = append(errs, fmt.Errorf("app %q dep %q: at least one path/paths entry is required", a.App, dep.To))
		}
		for pi, p := range candidates {
			if len(p.Hops) == 0 {
				errs = append(errs, fmt.Errorf("app %q dep %q path[%d]: must have at least one hop", a.App, dep.To, pi))
			}
			for _, h := range p.Hops {
				if !validDirections[h.Direction] {
					errs = append(errs, fmt.Errorf(
						"app %q dep %q path[%d]: invalid direction %q (must be one of ingress, egress, transit)",
						a.App, dep.To, pi, h.Direction))
				}
				if _, err := r.Resolve(h.Device, h.If); err != nil {
					errs = append(errs, fmt.Errorf("app %q dep %q path[%d]: %w", a.App, dep.To, pi, err))
				}
			}
		}
	}
	return errs
}

// keySafe rejects values that would corrupt the ':'-delimited identity keys
// derived from declaration fields: it fails on the ':' character and on any
// control character (which would also break key parsing and logging).
func keySafe(field, value string) error {
	if strings.ContainsRune(value, ':') {
		return fmt.Errorf("%s %q: must not contain ':'", field, value)
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return fmt.Errorf("%s %q: must not contain control characters", field, value)
		}
	}
	return nil
}

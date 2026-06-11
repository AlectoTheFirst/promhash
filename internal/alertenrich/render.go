package alertenrich

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// RenderCfg controls how impact rows become alert attachments.
type RenderCfg struct {
	Prefix       string // key prefix, e.g. "promhash_"
	EnrichLabels bool   // when false, no labels are produced (annotations only)
}

// critRank orders criticality strings; higher wins. Lookup is on the
// lowercased value (criticality is free-form, so "Tier-1" must rank like
// "tier-1"); unknown vocabularies and empty rank as 0. The common
// vocabularies are supported: severity words, the project's documented
// free-form tier convention (tier-1 = most critical), incident priorities
// (P1 = most critical), and sev levels (sev1 = most critical).
var critRank = map[string]int{
	"critical": 4, "high": 3, "medium": 2, "low": 1,
	"tier-1": 4, "tier-2": 3, "tier-3": 2, "tier-4": 1,
	"p1": 4, "p2": 3, "p3": 2, "p4": 1,
	"sev1": 4, "sev2": 3, "sev3": 2, "sev4": 1,
}

// Render turns impact rows into the labels and annotations to attach to an alert.
// Empty rows produce (nil, nil) so the alert passes through unchanged. When
// EnrichLabels is false, the labels map is nil but annotations are still built.
// Output is deterministic: rows are sorted by (app, customer) before rendering.
func Render(rows []graph.ImpactRow, cfg RenderCfg) (labels, annotations map[string]string) {
	if len(rows) == 0 {
		return nil, nil
	}

	sorted := append([]graph.ImpactRow(nil), rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].App != sorted[j].App {
			return sorted[i].App < sorted[j].App
		}
		return sorted[i].Customer < sorted[j].Customer
	})

	apps := map[string]struct{}{}
	customers := map[string]struct{}{}
	maxRank, maxCrit := 0, "unknown"
	var lines []string
	for _, r := range sorted {
		apps[r.App] = struct{}{}
		if r.Customer != "" {
			customers[r.Customer] = struct{}{}
		}
		// Rank case-insensitively but keep the declared casing in the label.
		if rk := critRank[strings.ToLower(r.Criticality)]; rk > maxRank {
			maxRank, maxCrit = rk, r.Criticality
		}
		line := fmt.Sprintf("- %s (%s) owner %s", r.App, r.Service, r.Owner)
		if r.Customer != "" {
			line += " customer " + r.Customer
		}
		if r.Criticality != "" {
			line += " [" + r.Criticality + "]"
		}
		lines = append(lines, line)
	}

	annotations = map[string]string{
		cfg.Prefix + "impact": fmt.Sprintf("apps affected (%d):\n%s", len(apps), strings.Join(lines, "\n")),
		cfg.Prefix + "blast_radius": fmt.Sprintf("%s, %s",
			plural(len(apps), "app"), plural(len(customers), "customer")),
	}

	if cfg.EnrichLabels {
		labels = map[string]string{
			cfg.Prefix + "max_criticality": maxCrit,
			cfg.Prefix + "app_count":       strconv.Itoa(len(apps)),
			cfg.Prefix + "customer_impact": strconv.FormatBool(len(customers) > 0),
		}
	}
	return labels, annotations
}

// plural renders "1 app" / "2 apps".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

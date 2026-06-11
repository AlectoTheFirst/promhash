package alertenrich

import (
	"reflect"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

func twoRows() []graph.ImpactRow {
	return []graph.ImpactRow{
		{App: "payments", Service: "payments-api", Owner: "team-payments", Customer: "acme", Criticality: "critical"},
		{App: "ledger", Service: "ledger-api", Owner: "team-ledger"},
	}
}

func TestRenderLabelsAndAnnotations(t *testing.T) {
	labels, annotations := Render(twoRows(), RenderCfg{Prefix: "promhash_", EnrichLabels: true})

	wantLabels := map[string]string{
		"promhash_max_criticality": "critical",
		"promhash_app_count":       "2",
		"promhash_customer_impact": "true",
	}
	if !reflect.DeepEqual(labels, wantLabels) {
		t.Fatalf("labels:\n got %+v\nwant %+v", labels, wantLabels)
	}

	wantImpact := "apps affected (2):\n" +
		"- ledger (ledger-api) owner team-ledger\n" +
		"- payments (payments-api) owner team-payments customer acme [critical]"
	if annotations["promhash_impact"] != wantImpact {
		t.Fatalf("promhash_impact:\n got:\n%s\nwant:\n%s", annotations["promhash_impact"], wantImpact)
	}
	if annotations["promhash_blast_radius"] != "2 apps, 1 customer" {
		t.Fatalf("blast_radius: %q", annotations["promhash_blast_radius"])
	}
}

func TestRenderEmptyRows(t *testing.T) {
	labels, annotations := Render(nil, RenderCfg{Prefix: "promhash_", EnrichLabels: true})
	if labels != nil || annotations != nil {
		t.Fatalf("expected nil maps for empty impact, got labels=%+v annotations=%+v", labels, annotations)
	}
}

func TestRenderLabelsDisabled(t *testing.T) {
	labels, annotations := Render(twoRows(), RenderCfg{Prefix: "promhash_", EnrichLabels: false})
	if labels != nil {
		t.Fatalf("expected nil labels when EnrichLabels false, got %+v", labels)
	}
	if annotations["promhash_blast_radius"] == "" {
		t.Fatal("annotations should still be produced when labels disabled")
	}
}

func TestRenderNoCriticalityNoCustomer(t *testing.T) {
	rows := []graph.ImpactRow{{App: "edge", Service: "edge-svc", Owner: "team-edge"}}
	labels, annotations := Render(rows, RenderCfg{Prefix: "promhash_", EnrichLabels: true})
	if labels["promhash_max_criticality"] != "unknown" {
		t.Fatalf("max_criticality: %q (want unknown)", labels["promhash_max_criticality"])
	}
	if labels["promhash_customer_impact"] != "false" {
		t.Fatalf("customer_impact: %q (want false)", labels["promhash_customer_impact"])
	}
	if annotations["promhash_blast_radius"] != "1 app, 0 customers" {
		t.Fatalf("blast_radius: %q", annotations["promhash_blast_radius"])
	}
}

// TestRenderTierCriticality: the project's documented criticality convention
// is free-form tier-N (tier-1 highest); the max selection must rank it, not
// fall through to "unknown".
func TestRenderTierCriticality(t *testing.T) {
	rows := []graph.ImpactRow{
		{App: "checkout", Service: "checkout-api", Owner: "team-shop", Customer: "globex", Criticality: "tier-2"},
		{App: "payments", Service: "payments-api", Owner: "team-payments", Criticality: "tier-1"},
	}
	labels, _ := Render(rows, RenderCfg{Prefix: "promhash_", EnrichLabels: true})
	if labels["promhash_max_criticality"] != "tier-1" {
		t.Fatalf("max_criticality = %q, want tier-1 (tier-1 outranks tier-2)", labels["promhash_max_criticality"])
	}
}

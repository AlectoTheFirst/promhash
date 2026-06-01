package declare

import "testing"

const sample = `
app: payments
runs_as: payments-api
owner: team-payments
consumed_by_customers: [acme, globex]
depends_on:
  - to: ledger-api
    paths:
      - hops:
          - {device: rtr-acc-fra-1, if: Te0/1/2, direction: egress}
          - {device: rtr-core-1, if: "uplink-ledger-dc", direction: transit}
`

// pathSugarSample declares a dependency using BOTH the `path:` single-candidate
// sugar and the `paths:` list. Candidates must return the `path:` entry first,
// then the `paths:` entries in order.
const pathSugarSample = `
app: billing
runs_as: billing-api
owner: team-billing
depends_on:
  - to: ledger-api
    path:
      hops:
        - {device: rtr-sugar-1, if: Te0/0/0, direction: egress}
    paths:
      - hops:
          - {device: rtr-list-a, if: Te0/0/1, direction: ingress}
      - hops:
          - {device: rtr-list-b, if: Te0/0/2, direction: transit}
`

func TestCandidatesPathSugarFirst(t *testing.T) {
	d, err := Parse([]byte(pathSugarSample))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DependsOn) != 1 {
		t.Fatalf("want 1 dependency, got %d", len(d.DependsOn))
	}
	dep := d.DependsOn[0]
	if dep.Path == nil {
		t.Fatal("expected the `path:` single-candidate sugar to be populated")
	}
	cands := dep.Candidates()
	// 1 from `path:` + 2 from `paths:` = 3 candidates.
	if len(cands) != 3 {
		t.Fatalf("want 3 candidates (path + 2 paths), got %d: %+v", len(cands), cands)
	}
	// The `path:` sugar candidate must come first.
	if got := cands[0].Hops[0].Device; got != "rtr-sugar-1" {
		t.Fatalf("expected `path:` sugar candidate first, got device %q", got)
	}
	// Then the `paths:` entries, in declaration order.
	if got := cands[1].Hops[0].Device; got != "rtr-list-a" {
		t.Fatalf("expected first `paths:` entry second, got device %q", got)
	}
	if got := cands[2].Hops[0].Device; got != "rtr-list-b" {
		t.Fatalf("expected second `paths:` entry third, got device %q", got)
	}
}

func TestParse(t *testing.T) {
	d, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if d.App != "payments" || d.RunsAs != "payments-api" {
		t.Fatalf("got %+v", d)
	}
	if len(d.DependsOn) != 1 || d.DependsOn[0].To != "ledger-api" {
		t.Fatal("dep parse")
	}
	if len(d.DependsOn[0].Paths[0].Hops) != 2 {
		t.Fatal("hops parse")
	}
	if d.DependsOn[0].Paths[0].Hops[0].If != "Te0/1/2" {
		t.Fatal("if parse")
	}
}

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

package enrich

import (
	"testing"

	"github.com/starkweb/promhash/internal/graph"
)

func TestFederationMatch(t *testing.T) {
	hops := []graph.Hop{
		{Instance: "10.0.0.2", IfIndex: 43}, {Instance: "10.0.0.1", IfIndex: 42}, {Instance: "10.0.0.1", IfIndex: 42},
	}
	got := FederationMatch(hops)
	want := `{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1|10.0.0.2", ifIndex=~"42|43"}`
	if got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
}

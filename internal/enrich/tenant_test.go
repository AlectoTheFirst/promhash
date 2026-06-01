package enrich

import (
	"os"
	"testing"
)

func TestTenantScrapeConfigGolden(t *testing.T) {
	got := TenantScrapeConfig("payments", "http://main-prometheus:9090",
		`{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1", ifIndex=~"42"}`)
	want, _ := os.ReadFile("testdata/tenant.golden.yaml")
	if got != string(want) {
		t.Fatalf("golden mismatch:\n%s", got)
	}
}

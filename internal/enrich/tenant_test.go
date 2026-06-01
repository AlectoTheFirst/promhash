package enrich

import (
	"os"
	"testing"
)

func TestTenantScrapeConfigGolden(t *testing.T) {
	got := TenantScrapeConfig("payments", "http://main-prometheus:9090",
		`{__name__=~"ifHC(In|Out)Octets|ifOperStatus", instance=~"10.0.0.1", ifIndex=~"42"}`)
	want, err := os.ReadFile("testdata/tenant.golden.yaml")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch:\n got:\n%s\nwant:\n%s", got, string(want))
	}
}

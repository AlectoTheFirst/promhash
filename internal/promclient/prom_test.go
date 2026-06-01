package promclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHarvestInterfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
          {"metric":{"instance":"10.0.0.1","ifIndex":"42","ifName":"Te0/1/2","ifDescr":"TenGigE0/1/2","ifAlias":"uplink-ledger-dc"},"value":[0,"1"]}
        ]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	rows, err := c.HarvestInterfaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].IfIndex != 42 || rows[0].Instance != "10.0.0.1" {
		t.Fatalf("got %+v", rows)
	}
}

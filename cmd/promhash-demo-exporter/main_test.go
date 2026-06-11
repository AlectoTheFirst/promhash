package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, device string, epoch, now int64) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics?device="+device, nil)
	writeMetrics(rec, req.URL.Query().Get("device"), epoch, now)
	body, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(body)
}

func TestMetricsForKnownDevice(t *testing.T) {
	code, body := get(t, "rtr-core-1", 0, 100)
	if code != http.StatusOK {
		t.Fatalf("code %d", code)
	}
	// 110e6 B/s * 100s on the trunk egress.
	if !strings.Contains(body, `ifHCOutOctets{ifIndex="21",ifName="Te0/1/0",ifDescr="TenGigE0/1/0",ifAlias="trunk-dc"} 11000000000`) {
		t.Fatalf("trunk egress counter missing/wrong:\n%s", body)
	}
	if !strings.Contains(body, `ifHighSpeed{ifIndex="21"`) {
		t.Fatalf("missing ifHighSpeed:\n%s", body)
	}
}

func TestUnknownDevice404(t *testing.T) {
	if code, _ := get(t, "nope", 0, 100); code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
}

func TestFlapStateCycle(t *testing.T) {
	// t=10: inside the down window of cycle 0.
	if oper, errs := flapState(10); oper != 2 || errs != 10 {
		t.Fatalf("t=10: oper=%d errs=%d, want 2/10", oper, errs)
	}
	// t=300: up, errors frozen at one full down-window.
	if oper, errs := flapState(300); oper != 1 || errs != 90 {
		t.Fatalf("t=300: oper=%d errs=%d, want 1/90", oper, errs)
	}
	// t=610: second cycle, down again, counter monotonic.
	if oper, errs := flapState(610); oper != 2 || errs != 100 {
		t.Fatalf("t=610: oper=%d errs=%d, want 2/100", oper, errs)
	}
}

func TestCountersMonotonic(t *testing.T) {
	_, a := get(t, "rtr-edge-1", 0, 50)
	_, b := get(t, "rtr-edge-1", 0, 60)
	if a == b {
		t.Fatal("counters did not advance between scrapes")
	}
}

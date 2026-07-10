package promclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("expected skipped==0, got %d", skipped)
	}
	if len(rows) != 1 || rows[0].IfIndex != 42 || rows[0].Instance != "10.0.0.1" {
		t.Fatalf("got %+v", rows)
	}
}

// TestHarvestNonVectorErrors verifies that a non-vector result type (matrix)
// returns a non-nil error rather than a silent (nil, nil) result.
func TestHarvestNonVectorErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A matrix result — two time series with ranges instead of instant values.
		w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
          {"metric":{"instance":"10.0.0.1"},"values":[[0,"1"],[1,"2"]]}
        ]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "")
	if err == nil {
		t.Fatalf("expected non-nil error for matrix result, got rows=%v skipped=%d", rows, skipped)
	}
}

// TestHarvestEmptyVectorNoError verifies that an empty vector result (zero
// series) is NOT an error — it is a valid response meaning no interfaces are
// currently known.
func TestHarvestEmptyVectorNoError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil error for empty vector, got %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows, got %d", len(rows))
	}
	if skipped != 0 {
		t.Fatalf("expected skipped==0, got %d", skipped)
	}
}

// TestHarvestBadIfIndexSkipped verifies that a series with a non-numeric
// ifIndex label is skipped (not appended with IfIndex=0), increments the
// skipped count, and returns the remaining valid rows without error.
func TestHarvestBadIfIndexSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
          {"metric":{"instance":"10.0.0.1","ifIndex":"42","ifName":"Te0/1","ifDescr":"","ifAlias":""},"value":[0,"1"]},
          {"metric":{"instance":"10.0.0.2","ifIndex":"abc","ifName":"Te0/2","ifDescr":"","ifAlias":""},"value":[0,"1"]}
        ]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	if rows[0].IfIndex != 42 {
		t.Fatalf("expected IfIndex 42, got %d", rows[0].IfIndex)
	}
	if skipped != 1 {
		t.Fatalf("expected skipped==1, got %d", skipped)
	}
}

// TestHarvestMissingIfIndexAllowed verifies that a series with NO ifIndex
// label is allowed (treated as IfIndex=0) and is NOT skipped. An absent label
// is different from a present-but-unparseable label.
func TestHarvestMissingIfIndexAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// No "ifIndex" key in metric at all.
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
          {"metric":{"instance":"10.0.0.1","ifName":"loopback0","ifDescr":"","ifAlias":""},"value":[0,"1"]}
        ]}}`))
	}))
	defer srv.Close()
	c, _ := New(srv.URL)
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].IfIndex != 0 {
		t.Fatalf("expected IfIndex 0 for missing label, got %d", rows[0].IfIndex)
	}
	if skipped != 0 {
		t.Fatalf("expected skipped==0, got %d", skipped)
	}
}

// TestHarvestRespectsContextDeadline checks that a short context deadline
// causes HarvestInterfaces to return promptly with an error rather than hang.
func TestHarvestRespectsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block well past the caller's deadline so the call must be cut short by
		// the context, not by the handler returning. (httptest Close does not wait
		// for in-flight handlers; this goroutine simply sleeps to completion and
		// never writes to w, so there is no leak or write-after-close.)
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, _, callErr := c.HarvestInterfaces(ctx, "")
	elapsed := time.Since(start)

	if callErr == nil {
		t.Fatal("expected an error from a blocked server with a short deadline, got nil")
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("HarvestInterfaces took %v; expected < 1s (context deadline should have fired)", elapsed)
	}
	// The error should chain to context.DeadlineExceeded so callers can introspect it.
	if !errors.Is(callErr, context.DeadlineExceeded) {
		t.Errorf("error does not chain to context.DeadlineExceeded (got %v)", callErr)
	}
}

// TestClientTimeoutFires checks that NewWithTimeout wires an http.Client.Timeout
// so that a stalled server is cut off even when the caller passes context.Background()
// (i.e. a context with no deadline of its own).
func TestClientTimeoutFires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep long enough that only the client timeout will fire.
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewWithTimeout(srv.URL, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, _, callErr := c.HarvestInterfaces(context.Background(), "")
	elapsed := time.Since(start)

	if callErr == nil {
		t.Fatal("expected an error from client timeout, got nil")
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("HarvestInterfaces took %v; expected < 1s (http.Client.Timeout should have fired)", elapsed)
	}
}

// TestHarvestDeviceLabel asserts the device label is added to the harvest
// grouping and surfaced as IfaceRow.Device.
func TestHarvestDeviceLabel(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotQuery = r.Form.Get("query")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
{"metric":{"instance":"10.0.0.1:161","hostname":"rtr-core-1","ifIndex":"42","ifName":"Te0/1/2"},"value":[1,"1"]}
]}}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	rows, skipped, err := c.HarvestInterfaces(context.Background(), "hostname")
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 || len(rows) != 1 {
		t.Fatalf("rows=%d skipped=%d", len(rows), skipped)
	}
	if rows[0].Device != "rtr-core-1" || rows[0].Instance != "10.0.0.1:161" {
		t.Fatalf("row %+v", rows[0])
	}
	if !strings.Contains(gotQuery, ", hostname)") {
		t.Fatalf("harvest query missing device label grouping: %q", gotQuery)
	}
}

// TestHarvestInvalidDeviceLabelRejected asserts a non-label-grammar name is
// refused before any query is issued (injection guard).
func TestHarvestInvalidDeviceLabelRejected(t *testing.T) {
	c, err := New("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.HarvestInterfaces(context.Background(), `x) or (up`); err == nil {
		t.Fatal("expected error for invalid device label name")
	}
}

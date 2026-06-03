package promclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	rows, skipped, err := c.HarvestInterfaces(context.Background())
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
	rows, skipped, err := c.HarvestInterfaces(context.Background())
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
	rows, skipped, err := c.HarvestInterfaces(context.Background())
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
	rows, skipped, err := c.HarvestInterfaces(context.Background())
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
	rows, skipped, err := c.HarvestInterfaces(context.Background())
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
	_, _, callErr := c.HarvestInterfaces(ctx)
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
	_, _, callErr := c.HarvestInterfaces(context.Background())
	elapsed := time.Since(start)

	if callErr == nil {
		t.Fatal("expected an error from client timeout, got nil")
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("HarvestInterfaces took %v; expected < 1s (http.Client.Timeout should have fired)", elapsed)
	}
}

// capHandler returns an http.HandlerFunc that serves instant-query JSON for
// the two CapacityStatus queries. speedJSON and statusJSON are the "result"
// arrays for ifHighSpeed and ifOperStatus respectively.
func capHandler(t *testing.T, speedJSON, statusJSON string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("capHandler: ParseForm: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		q := r.FormValue("query")
		w.Header().Set("Content-Type", "application/json")
		var resultJSON string
		switch {
		case q == capSpeedQuery:
			resultJSON = speedJSON
		case q == capStatusQuery:
			resultJSON = statusJSON
		default:
			// Fall through to HarvestInterfaces query or unknown — return empty.
			resultJSON = "[]"
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":` + resultJSON + `}}`))
	}
}

// TestCapacityStatusBothVectors verifies the happy path: when ifHighSpeed and
// ifOperStatus each return a row per interface, CapacityStatus returns one
// CapRow per (instance, ifIndex) with matching Speed and OperStatus.
func TestCapacityStatusBothVectors(t *testing.T) {
	speedJSON := `[
		{"metric":{"instance":"10.0.0.1","ifIndex":"7"},"value":[0,"10000"]},
		{"metric":{"instance":"10.0.0.2","ifIndex":"9"},"value":[0,"1000"]}
	]`
	statusJSON := `[
		{"metric":{"instance":"10.0.0.1","ifIndex":"7"},"value":[0,"1"]},
		{"metric":{"instance":"10.0.0.2","ifIndex":"9"},"value":[0,"1"]}
	]`
	srv := httptest.NewServer(capHandler(t, speedJSON, statusJSON))
	defer srv.Close()

	c, _ := New(srv.URL)
	rows, skipped, err := c.CapacityStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("expected skipped==0, got %d", skipped)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}
	// Rows are sorted by (instance, ifIndex): (10.0.0.1,7) then (10.0.0.2,9).
	r0 := rows[0]
	if r0.Instance != "10.0.0.1" || r0.IfIndex != 7 {
		t.Fatalf("row[0]: expected (10.0.0.1, 7), got (%s, %d)", r0.Instance, r0.IfIndex)
	}
	if r0.SpeedMbps != 10000 {
		t.Fatalf("row[0]: expected SpeedMbps=10000, got %v", r0.SpeedMbps)
	}
	if r0.OperStatus != 1 {
		t.Fatalf("row[0]: expected OperStatus=1, got %v", r0.OperStatus)
	}
	r1 := rows[1]
	if r1.Instance != "10.0.0.2" || r1.IfIndex != 9 {
		t.Fatalf("row[1]: expected (10.0.0.2, 9), got (%s, %d)", r1.Instance, r1.IfIndex)
	}
	if r1.SpeedMbps != 1000 {
		t.Fatalf("row[1]: expected SpeedMbps=1000, got %v", r1.SpeedMbps)
	}
	if r1.OperStatus != 1 {
		t.Fatalf("row[1]: expected OperStatus=1, got %v", r1.OperStatus)
	}
}

// TestCapacityStatusAbsentOperStatus verifies the outer-join behavior: an
// interface present in ifHighSpeed but absent from ifOperStatus still produces
// a CapRow with OperStatus==0.
func TestCapacityStatusAbsentOperStatus(t *testing.T) {
	// ifHighSpeed has (10.0.0.1, 7).
	speedJSON := `[
		{"metric":{"instance":"10.0.0.1","ifIndex":"7"},"value":[0,"10000"]}
	]`
	// ifOperStatus has a DIFFERENT interface only — (10.0.0.2, ifIndex 99).
	statusJSON := `[
		{"metric":{"instance":"10.0.0.2","ifIndex":"99"},"value":[0,"1"]}
	]`
	srv := httptest.NewServer(capHandler(t, speedJSON, statusJSON))
	defer srv.Close()

	c, _ := New(srv.URL)
	rows, skipped, err := c.CapacityStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 0 {
		t.Fatalf("expected skipped==0, got %d", skipped)
	}
	// Two rows: (10.0.0.1,7) from speed-only, (10.0.0.2,99) from status-only.
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}
	// Find the (10.0.0.1,7) row — it must have SpeedMbps set and OperStatus==0.
	var found bool
	for _, r := range rows {
		if r.Instance == "10.0.0.1" && r.IfIndex == 7 {
			found = true
			if r.SpeedMbps != 10000 {
				t.Fatalf("(10.0.0.1,7): expected SpeedMbps=10000, got %v", r.SpeedMbps)
			}
			if r.OperStatus != 0 {
				t.Fatalf("(10.0.0.1,7): expected OperStatus==0 (absent from ifOperStatus), got %v", r.OperStatus)
			}
		}
	}
	if !found {
		t.Fatalf("(10.0.0.1,7) row missing from results: %+v", rows)
	}
}

// TestCapacityStatusBadIfIndexSkipped verifies that a series with a non-numeric
// ifIndex in either vector is skipped and counted.
func TestCapacityStatusBadIfIndexSkipped(t *testing.T) {
	speedJSON := `[
		{"metric":{"instance":"10.0.0.1","ifIndex":"7"},"value":[0,"10000"]},
		{"metric":{"instance":"10.0.0.1","ifIndex":"not-a-number"},"value":[0,"1000"]}
	]`
	statusJSON := `[
		{"metric":{"instance":"10.0.0.1","ifIndex":"7"},"value":[0,"1"]}
	]`
	srv := httptest.NewServer(capHandler(t, speedJSON, statusJSON))
	defer srv.Close()

	c, _ := New(srv.URL)
	rows, skipped, err := c.CapacityStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d: %+v", len(rows), rows)
	}
	if skipped != 1 {
		t.Fatalf("expected skipped==1, got %d", skipped)
	}
}

// TestCapacityStatusNonVectorError verifies that a non-vector result type from
// the ifHighSpeed query surfaces as an error (same as HarvestInterfaces).
func TestCapacityStatusNonVectorError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Always return a matrix regardless of which query is being made.
		w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[
			{"metric":{"instance":"10.0.0.1"},"values":[[0,"1"],[1,"2"]]}
		]}}`))
	}))
	defer srv.Close()

	c, _ := New(srv.URL)
	rows, skipped, err := c.CapacityStatus(context.Background())
	if err == nil {
		t.Fatalf("expected non-nil error for matrix result, got rows=%v skipped=%d", rows, skipped)
	}
}

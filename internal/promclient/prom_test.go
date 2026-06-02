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
	rows, err := c.HarvestInterfaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].IfIndex != 42 || rows[0].Instance != "10.0.0.1" {
		t.Fatalf("got %+v", rows)
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
	_, callErr := c.HarvestInterfaces(ctx)
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
	_, callErr := c.HarvestInterfaces(context.Background())
	elapsed := time.Since(start)

	if callErr == nil {
		t.Fatal("expected an error from client timeout, got nil")
	}
	if elapsed >= 1*time.Second {
		t.Fatalf("HarvestInterfaces took %v; expected < 1s (http.Client.Timeout should have fired)", elapsed)
	}
}

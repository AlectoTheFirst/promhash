package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/httpx"
)

// newReqFactory returns a request factory for a GET to url, bound to ctx.
func newReqFactory(ctx context.Context, url string) func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	}
}

// TestRetry503Then200 verifies that a 503 on the first attempt is retried and
// a subsequent 200 is returned. The handler must be hit exactly twice.
func TestRetry503Then200(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected 2 hits, got %d", n)
	}
}

// TestNoRetryOn400 verifies that a 400 response is returned to the caller
// immediately without retrying (exactly one hit).
func TestNoRetryOn400(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected response (not error) on 400, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected 1 hit on 400, got %d", n)
	}
}

// TestNoRetryOn404 verifies that a 404 response is returned immediately (single hit).
func TestNoRetryOn404(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected response (not error) on 404, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("expected 1 hit on 404, got %d", n)
	}
}

// TestCtxCancelMidBackoff verifies that cancelling the context during a backoff
// window returns promptly with a context error, not after exhausting all retries.
func TestCtxCancelMidBackoff(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Use a large base so the backoff window is long; cancel after the first hit.
	// We run DoWithRetry in a goroutine and cancel from outside.
	done := make(chan error, 1)
	go func() {
		// Use a large base backoff (100ms) so we have time to cancel before the
		// second attempt fires.
		_, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 100*time.Millisecond)
		done <- err
	}()

	// Wait until the first hit lands, then cancel.
	for hits.Load() == 0 {
		time.Sleep(1 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DoWithRetry did not return promptly after context cancel")
	}
}

// TestRetryAfterHonored verifies that a 503 response with Retry-After: 0 is
// retried (the header is honored and we don't treat it as non-retriable).
func TestRetryAfterHonored(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected 2 hits (retry), got %d", n)
	}
}

// TestAllAttemptsExhausted verifies that when all attempts return retriable
// errors, DoWithRetry returns the last response (not nil) with no Go error.
func TestAllAttemptsExhausted(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected last response (not error) when attempts exhausted, got: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response when attempts exhausted")
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 3 {
		t.Fatalf("expected 3 hits (all attempts), got %d", n)
	}
}

// TestRetryOn429 verifies that HTTP 429 is retried.
func TestRetryOn429(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx := context.Background()
	resp, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("expected 2 hits, got %d", n)
	}
}

// TestContextAlreadyCancelled verifies that if the context is already cancelled
// before the first attempt, DoWithRetry returns a context error immediately.
func TestContextAlreadyCancelled(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the call

	_, err := httpx.DoWithRetry(ctx, http.DefaultClient, newReqFactory(ctx, srv.URL), 3, 1*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

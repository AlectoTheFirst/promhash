// Package httpx provides shared HTTP helpers for the promhash clients.
package httpx

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// maxRetryAfter caps how long we will sleep when honoring a Retry-After header,
// regardless of the value the server sends.
const maxRetryAfter = 30 * time.Second

// isRetriableStatus reports whether the HTTP status code should trigger a retry.
// 429, 500, 502, 503, 504 are retriable; all other codes are returned as-is.
func isRetriableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

// isCtxError reports whether err is a context cancellation or deadline error.
func isCtxError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// retryAfterDelay parses the Retry-After header from resp and returns the delay
// to wait before retrying. If the header is absent or unparseable the returned
// ok is false and the caller should use its computed backoff instead.
// The returned delay is capped to maxRetryAfter.
func retryAfterDelay(resp *http.Response) (time.Duration, bool) {
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0, false
	}
	// Try delta-seconds form first.
	if secs, err := strconv.ParseFloat(val, 64); err == nil {
		d := time.Duration(secs * float64(time.Second))
		if d < 0 {
			d = 0
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}
	// Try HTTP-date form.
	if t, err := http.ParseTime(val); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		if d > maxRetryAfter {
			d = maxRetryAfter
		}
		return d, true
	}
	return 0, false
}

// DoWithRetry issues an HTTP request using newReq to produce a fresh
// *http.Request for each attempt. It retries on transport errors (provided
// they are not context errors) and on retriable HTTP status codes (429, 500,
// 502, 503, 504).
//
// attempts is the total number of tries (the initial attempt plus up to
// attempts-1 retries). base is the starting backoff duration; the actual sleep
// before attempt n is base*2^(n-1) plus random jitter up to 50% of that value.
// If the response carries a Retry-After header the server-requested delay is
// used instead of the computed backoff, capped to maxRetryAfter.
//
// The sleep is interruptible: if ctx is cancelled during a backoff window,
// DoWithRetry returns promptly with a context error.
//
// When all attempts are exhausted the last response (still open) and a nil
// error are returned so the caller can read the body and decide what to do.
// Non-retriable responses (e.g. 400, 404) are returned immediately on the
// first attempt. Context errors from hc.Do are returned immediately.
func DoWithRetry(ctx context.Context, hc *http.Client, newReq func() (*http.Request, error), attempts int, base time.Duration) (*http.Response, error) {
	if attempts <= 0 {
		attempts = 1
	}

	for attempt := 0; attempt < attempts; attempt++ {
		// Check for cancellation before building the next request.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		req, err := newReq()
		if err != nil {
			return nil, err
		}

		resp, err := hc.Do(req)
		if err != nil {
			if isCtxError(err) {
				return nil, err
			}
			// Transport error: retry if we have attempts remaining.
			if attempt == attempts-1 {
				return nil, err
			}
			if sleepErr := backoffSleep(ctx, attempt, base, 0); sleepErr != nil {
				return nil, sleepErr
			}
			continue
		}

		if !isRetriableStatus(resp.StatusCode) {
			// Non-retriable: return as-is (2xx success, 4xx client errors, etc.).
			return resp, nil
		}

		// Retriable status code.
		if attempt == attempts-1 {
			// Last attempt: return the response so the caller can inspect it.
			return resp, nil
		}

		// Parse Retry-After before draining/closing the body so the data flow
		// is unambiguous (retryAfterDelay only reads headers, but we want it
		// called while the response is still logically "open").
		retryAfter, hasRetryAfter := retryAfterDelay(resp)

		// Drain and close the body so the connection can be reused.
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10)) //nolint:errcheck
		resp.Body.Close()

		var serverDelay time.Duration
		if hasRetryAfter {
			serverDelay = retryAfter
		}
		if sleepErr := backoffSleep(ctx, attempt, base, serverDelay); sleepErr != nil {
			return nil, sleepErr
		}
	}

	panic("httpx: unreachable")
}

// backoffSleep sleeps for the appropriate backoff duration before the next
// attempt. serverDelay is the parsed Retry-After value (0 means absent); when
// non-zero it is used instead of the computed exponential backoff. Otherwise
// exponential backoff with jitter is applied.
// Returns a non-nil error if ctx is cancelled during the sleep.
func backoffSleep(ctx context.Context, attempt int, base time.Duration, serverDelay time.Duration) error {
	delay := serverDelay
	if delay == 0 {
		// Exponential backoff: base * 2^attempt.
		exp := base
		for i := 0; i < attempt; i++ {
			exp *= 2
		}
		// Add jitter: up to 50% of the computed value.
		jitter := time.Duration(rand.Int64N(int64(exp/2) + 1))
		delay = exp + jitter
	}

	if delay <= 0 {
		return ctx.Err() // handle the Retry-After: 0 case — check ctx but don't sleep
	}

	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

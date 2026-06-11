package alertenrich

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// ImpactClient is the proxy's read-only view of the graph. Implementations must
// map "interface not found" (and ambiguous) to (nil, nil) — no impact — so the
// proxy fails open; only genuine transport/server errors return a non-nil error.
type ImpactClient interface {
	ImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error)
	ImpactByName(ctx context.Context, device, ifName string, at time.Time) ([]graph.ImpactRow, error)
}

// apiClient calls the promhash-api C7 /impact endpoint.
type apiClient struct {
	base  string
	hc    *http.Client
	token string
}

// NewAPIClient returns an ImpactClient backed by the C7 API at base (e.g.
// "http://promhash-api:8080"). hc may be nil, in which case http.DefaultClient
// is used (callers should pass a client with a sane timeout in production).
// token, when non-empty, is sent as an "Authorization: Bearer <token>" header —
// promhash-api requires one unless it runs with -insecure-no-auth.
func NewAPIClient(base string, hc *http.Client, token string) ImpactClient {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &apiClient{base: base, hc: hc, token: token}
}

func (c *apiClient) ImpactByInstanceIndex(ctx context.Context, instance string, ifIndex int, at time.Time) ([]graph.ImpactRow, error) {
	q := url.Values{"instance": {instance}, "ifIndex": {strconv.Itoa(ifIndex)}}
	return c.query(ctx, q, at)
}

func (c *apiClient) ImpactByName(ctx context.Context, device, ifName string, at time.Time) ([]graph.ImpactRow, error) {
	q := url.Values{"device": {device}, "ifName": {ifName}}
	return c.query(ctx, q, at)
}

// query issues GET /impact with q (+ optional at) and decodes the wrapped body.
// 404/409 (no-match / ambiguous) map to empty rows so an un-mappable interface
// fails open. Any other non-2xx is an error.
func (c *apiClient) query(ctx context.Context, q url.Values, at time.Time) ([]graph.ImpactRow, error) {
	if !at.IsZero() {
		q.Set("at", strconv.FormatInt(at.Unix(), 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/impact?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		var body struct {
			Impact []graph.ImpactRow `json:"impact"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return nil, err
		}
		return body.Impact, nil
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusConflict:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, nil // fail open: treat as no impact
	default:
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("promhash-api /impact: status %d", resp.StatusCode)
	}
}

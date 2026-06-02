// Package nautobot provides a minimal client for querying a Nautobot
// instance's REST API, used to resolve device inventory into the
// management IPs that Prometheus targets as scrape instances.
package nautobot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxBodyBytes caps the response body read from Nautobot to guard against
// an unexpectedly large or hostile payload exhausting memory.
const maxBodyBytes = 16 << 20

// maxErrBytes is the maximum number of bytes read from an error response body
// to include in the returned error message.
const maxErrBytes = 4 << 10

// nbPageSize is the number of results requested per page. It is a package-level
// var (not const) so that tests can override it to a smaller value.
var nbPageSize = 1000

// nbMaxPages caps the total number of pages fetched in a single call to
// DeviceInstanceMap to prevent an infinite loop against a misbehaving server.
var nbMaxPages = 1000

// Client talks to a Nautobot REST API using a base URL and an optional
// API token for authentication.
type Client struct {
	base, token string
	baseHost    string // parsed host of base URL, used for next-link validation
	hc          *http.Client
}

// New returns a Client for the given Nautobot base URL and API token.
// Any trailing slash on base is trimmed, and the underlying HTTP client
// is configured with a 30-second request timeout. An empty token results
// in unauthenticated requests.
func New(base, token string) *Client {
	trimmed := strings.TrimRight(base, "/")
	var baseHost string
	if u, err := url.Parse(trimmed); err == nil {
		baseHost = u.Host
	}
	return &Client{
		base:     trimmed,
		token:    token,
		baseHost: baseHost,
		hc:       &http.Client{Timeout: 30 * time.Second},
	}
}

type deviceList struct {
	Next    string `json:"next"`
	Results []struct {
		Name      string `json:"name"`
		PrimaryIP *struct {
			Address string `json:"address"`
		} `json:"primary_ip"`
	} `json:"results"`
}

// DeviceInstanceMap returns device name -> management IP (the Prometheus `instance` host).
//
// It follows Nautobot's cursor/offset pagination via the "next" field in each
// response envelope, stopping when "next" is empty or null. Before following
// any "next" URL, it validates that the host matches the configured base URL's
// host; a mismatch returns an error without issuing the request (to prevent
// token leakage to attacker-controlled redirects). At most nbMaxPages pages
// are fetched; exceeding this limit returns an error.
func (c *Client) DeviceInstanceMap(ctx context.Context) (map[string]string, error) {
	nextURL := fmt.Sprintf("%s/api/dcim/devices/?limit=%d", c.base, nbPageSize)

	out := map[string]string{}
	for page := 0; page < nbMaxPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Token "+c.token)
		}

		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBytes))
			resp.Body.Close()
			return nil, fmt.Errorf("nautobot: status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
		}

		var dl deviceList
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&dl); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, d := range dl.Results {
			if d.PrimaryIP != nil {
				out[d.Name] = strings.SplitN(d.PrimaryIP.Address, "/", 2)[0]
			}
		}

		if dl.Next == "" {
			return out, nil
		}

		// Validate that the next URL's host matches the configured base host,
		// to prevent token leakage when following an attacker-controlled redirect.
		nextParsed, err := url.Parse(dl.Next)
		if err != nil {
			return nil, fmt.Errorf("nautobot: invalid next URL %q: %w", dl.Next, err)
		}
		if nextParsed.Host != c.baseHost {
			return nil, fmt.Errorf("nautobot: next URL host %q does not match configured host %q", nextParsed.Host, c.baseHost)
		}

		nextURL = dl.Next
	}

	return nil, fmt.Errorf("nautobot: exceeded maximum page limit (%d pages)", nbMaxPages)
}

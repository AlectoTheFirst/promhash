// Package nautobot provides a minimal client for querying a Nautobot
// instance's REST API, used to resolve device inventory into the
// management IPs that Prometheus targets as scrape instances.
package nautobot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps the response body read from Nautobot to guard against
// an unexpectedly large or hostile payload exhausting memory.
const maxBodyBytes = 16 << 20

// Client talks to a Nautobot REST API using a base URL and an optional
// API token for authentication.
type Client struct {
	base, token string
	hc          *http.Client
}

// New returns a Client for the given Nautobot base URL and API token.
// Any trailing slash on base is trimmed, and the underlying HTTP client
// is configured with a 30-second request timeout. An empty token results
// in unauthenticated requests.
func New(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, hc: &http.Client{Timeout: 30 * time.Second}}
}

type deviceList struct {
	Results []struct {
		Name      string `json:"name"`
		PrimaryIP *struct {
			Address string `json:"address"`
		} `json:"primary_ip"`
	} `json:"results"`
}

// DeviceInstanceMap returns device name -> management IP (the Prometheus `instance` host).
func (c *Client) DeviceInstanceMap(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/dcim/devices/?limit=0", nil)
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
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nautobot: status %d", resp.StatusCode)
	}
	var dl deviceList
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&dl); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, d := range dl.Results {
		if d.PrimaryIP != nil {
			out[d.Name] = strings.SplitN(d.PrimaryIP.Address, "/", 2)[0]
		}
	}
	return out, nil
}

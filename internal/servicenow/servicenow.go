// Package servicenow provides a minimal client for reading configuration
// items from a ServiceNow instance via its Table API.
package servicenow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxBodyBytes caps the response body read from ServiceNow to guard against
// an unexpectedly large or hostile payload exhausting memory.
const maxBodyBytes = 16 << 20

// maxErrBytes is the maximum number of bytes read from an error response body
// to include in the returned error message.
const maxErrBytes = 4 << 10

// snPageSize is the number of results requested per page. It is a package-level
// var (not const) so that tests can override it to a smaller value without
// needing network round-trips for thousands of rows.
var snPageSize = 1000

// snMaxPages caps the total number of pages fetched in a single call to
// Applications to prevent an infinite loop against a misbehaving server.
var snMaxPages = 1000

// Client is a ServiceNow Table API client that authenticates with HTTP basic
// auth and reuses a single underlying http.Client for all requests.
type Client struct {
	base, user, pass string
	hc               *http.Client
}

// New returns a Client targeting the ServiceNow instance at base, using user
// and pass for basic authentication. Any trailing slash on base is trimmed,
// and the underlying HTTP client is configured with a 30 second timeout.
func New(base, user, pass string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), user: user, pass: pass, hc: &http.Client{Timeout: 30 * time.Second}}
}

// Application represents an application configuration item (cmdb_ci_appl)
// retrieved from ServiceNow.
type Application struct {
	// Name is the display name of the application CI.
	Name string
	// SysID is the ServiceNow record sys_id uniquely identifying the CI.
	SysID string
	// Service is the associated service, sourced from the u_app_service field.
	Service string
}

type appResp struct {
	Result []struct {
		Name    string `json:"name"`
		SysID   string `json:"sys_id"`
		Service string `json:"u_app_service"`
	} `json:"result"`
}

// Applications fetches all application configuration items from the
// cmdb_ci_appl table and returns them as a slice of Application. The provided
// context governs cancellation of the underlying HTTP request.
//
// It uses offset pagination (sysparm_limit / sysparm_offset) to retrieve all
// rows, stopping when a page returns fewer than snPageSize results. At most
// snMaxPages pages are fetched; exceeding this limit returns an error.
func (c *Client) Applications(ctx context.Context) ([]Application, error) {
	baseURL, err := url.Parse(c.base + "/api/now/table/cmdb_ci_appl")
	if err != nil {
		return nil, fmt.Errorf("servicenow: parse base URL: %w", err)
	}

	var out []Application
	for page := 0; page < snMaxPages; page++ {
		// Check for context cancellation between pages (not just inside Do).
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		q := baseURL.Query()
		q.Set("sysparm_limit", strconv.Itoa(snPageSize))
		q.Set("sysparm_offset", strconv.Itoa(page*snPageSize))
		pageURL := *baseURL
		pageURL.RawQuery = q.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
		if err != nil {
			return nil, err
		}
		req.SetBasicAuth(c.user, c.pass)

		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBytes))
			resp.Body.Close()
			return nil, fmt.Errorf("servicenow: status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
		}

		var ar appResp
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&ar); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		for _, a := range ar.Result {
			out = append(out, Application{Name: a.Name, SysID: a.SysID, Service: a.Service})
		}

		// A page shorter than snPageSize means we've reached the last page.
		if len(ar.Result) < snPageSize {
			return out, nil
		}
	}

	return nil, fmt.Errorf("servicenow: exceeded maximum page limit (%d pages)", snMaxPages)
}

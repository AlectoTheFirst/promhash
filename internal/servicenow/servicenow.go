// Package servicenow provides a minimal client for reading configuration
// items from a ServiceNow instance via its Table API.
package servicenow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxBodyBytes caps the response body read from ServiceNow to guard against
// an unexpectedly large or hostile payload exhausting memory.
const maxBodyBytes = 16 << 20

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
func (c *Client) Applications(ctx context.Context) ([]Application, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/now/table/cmdb_ci_appl", nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.pass)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("servicenow: status %d", resp.StatusCode)
	}
	var ar appResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&ar); err != nil {
		return nil, err
	}
	out := make([]Application, 0, len(ar.Result))
	for _, a := range ar.Result {
		out = append(out, Application{Name: a.Name, SysID: a.SysID, Service: a.Service})
	}
	return out, nil
}

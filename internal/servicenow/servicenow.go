package servicenow

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base, user, pass string
	hc               *http.Client
}

func New(base, user, pass string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), user: user, pass: pass, hc: &http.Client{Timeout: 30 * time.Second}}
}

type Application struct{ Name, SysID, Service string }

type appResp struct {
	Result []struct {
		Name    string `json:"name"`
		SysID   string `json:"sys_id"`
		Service string `json:"u_app_service"`
	} `json:"result"`
}

func (c *Client) Applications(ctx context.Context) ([]Application, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/now/table/cmdb_ci_appl", nil)
	req.SetBasicAuth(c.user, c.pass)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var ar appResp
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, err
	}
	out := make([]Application, 0, len(ar.Result))
	for _, a := range ar.Result {
		out = append(out, Application{Name: a.Name, SysID: a.SysID, Service: a.Service})
	}
	return out, nil
}

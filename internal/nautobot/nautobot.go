package nautobot

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	base, token string
	hc          *http.Client
}

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
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/dcim/devices/?limit=0", nil)
	if c.token != "" {
		req.Header.Set("Authorization", "Token "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var dl deviceList
	if err := json.NewDecoder(resp.Body).Decode(&dl); err != nil {
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

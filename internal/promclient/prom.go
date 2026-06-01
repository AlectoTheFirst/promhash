package promclient

import (
	"context"
	"strconv"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

type IfaceRow struct {
	Instance, IfName, IfDescr, IfAlias string
	IfIndex                            int
}

type Client struct{ api v1.API }

func New(addr string) (*Client, error) {
	c, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	return &Client{api: v1.NewAPI(c)}, nil
}

const harvestQuery = `group by (instance, ifIndex, ifName, ifDescr, ifAlias) (ifHCInOctets)`

func (c *Client) HarvestInterfaces(ctx context.Context) ([]IfaceRow, error) {
	val, _, err := c.api.Query(ctx, harvestQuery, time.Time{})
	if err != nil {
		return nil, err
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return nil, nil
	}
	out := make([]IfaceRow, 0, len(vec))
	for _, s := range vec {
		idx, _ := strconv.Atoi(string(s.Metric["ifIndex"]))
		out = append(out, IfaceRow{
			Instance: string(s.Metric["instance"]), IfName: string(s.Metric["ifName"]),
			IfDescr: string(s.Metric["ifDescr"]), IfAlias: string(s.Metric["ifAlias"]), IfIndex: idx})
	}
	return out, nil
}

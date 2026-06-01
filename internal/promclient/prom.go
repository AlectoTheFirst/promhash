// Package promclient provides a thin client for querying a Prometheus
// server to harvest network interface metadata from SNMP-derived metrics.
package promclient

import (
	"context"
	"strconv"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// IfaceRow holds the identifying labels for a single network interface as
// reported by a Prometheus target. The string fields correspond to the
// instance and SNMP ifTable labels (ifName, ifDescr, ifAlias), while IfIndex
// is the numeric SNMP ifIndex.
type IfaceRow struct {
	Instance, IfName, IfDescr, IfAlias string
	IfIndex                            int
}

// Client wraps a Prometheus HTTP API for harvesting interface metadata.
type Client struct{ api v1.API }

// New constructs a Client targeting the Prometheus server at addr. It returns
// an error if the address cannot be used to build the underlying API client.
func New(addr string) (*Client, error) {
	c, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	return &Client{api: v1.NewAPI(c)}, nil
}

const harvestQuery = `group by (instance, ifIndex, ifName, ifDescr, ifAlias) (ifHCInOctets)`

// HarvestInterfaces queries Prometheus for the set of known interfaces and
// returns one IfaceRow per series. It evaluates the harvest query at the
// server's current time and returns a nil slice (without error) when the
// result is not a vector.
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

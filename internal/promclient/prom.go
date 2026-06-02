// Package promclient provides a thin client for querying a Prometheus
// server to harvest network interface metadata from SNMP-derived metrics.
package promclient

import (
	"context"
	"fmt"
	"net/http"
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

// New constructs a Client targeting the Prometheus server at addr with a
// default overall HTTP timeout of 30 seconds. It returns an error if the
// address cannot be used to build the underlying API client.
func New(addr string) (*Client, error) {
	return NewWithTimeout(addr, 30*time.Second)
}

// NewWithTimeout constructs a Client targeting the Prometheus server at addr
// and applies timeout as the overall http.Client.Timeout (covers connect,
// TLS, and body read combined). It returns an error if the address cannot be
// used to build the underlying API client.
func NewWithTimeout(addr string, timeout time.Duration) (*Client, error) {
	httpClient := &http.Client{Timeout: timeout}
	c, err := promapi.NewClient(promapi.Config{Address: addr, Client: httpClient})
	if err != nil {
		return nil, err
	}
	return &Client{api: v1.NewAPI(c)}, nil
}

const harvestQuery = `group by (instance, ifIndex, ifName, ifDescr, ifAlias) (ifHCInOctets)`

// HarvestInterfaces queries Prometheus for the set of known interfaces and
// returns one IfaceRow per series. skipped counts series whose ifIndex label
// was present but non-numeric (those rows are not appended). An absent ifIndex
// label is allowed and yields IfIndex=0. An empty vector (zero series) is not
// an error. A non-vector result type (matrix, scalar, string) is an error.
func (c *Client) HarvestInterfaces(ctx context.Context) (rows []IfaceRow, skipped int, err error) {
	val, _, err := c.api.Query(ctx, harvestQuery, time.Time{})
	if err != nil {
		return nil, 0, err
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return nil, 0, fmt.Errorf("promclient: unexpected query result type %T (want vector)", val)
	}
	out := make([]IfaceRow, 0, len(vec))
	for _, s := range vec {
		idxStr := string(s.Metric["ifIndex"])
		var idx int
		if idxStr != "" {
			var perr error
			idx, perr = strconv.Atoi(idxStr)
			if perr != nil {
				skipped++
				continue
			}
		}
		out = append(out, IfaceRow{
			Instance: string(s.Metric["instance"]), IfName: string(s.Metric["ifName"]),
			IfDescr: string(s.Metric["ifDescr"]), IfAlias: string(s.Metric["ifAlias"]), IfIndex: idx})
	}
	return out, skipped, nil
}

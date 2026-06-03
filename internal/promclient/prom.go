// Package promclient provides a thin client for querying a Prometheus
// server to harvest network interface metadata from SNMP-derived metrics.
package promclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
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

// CapRow holds the interface capacity and operational status for a single
// interface as harvested from Prometheus. SpeedMbps comes from ifHighSpeed
// and OperStatus from ifOperStatus (1=up, 2=down per RFC 2863).
// A row is present if the interface appears in either metric; OperStatus
// defaults to 0 when absent from ifOperStatus.
type CapRow struct {
	Instance   string
	IfIndex    int
	SpeedMbps  float64
	OperStatus float64
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

// queryAttempts is the number of total tries for the Prometheus Query call.
// It is a package-level var so tests can reduce it without subclassing.
var queryAttempts = 3

// queryBase is the initial backoff duration for retries on the Prometheus Query call.
var queryBase = 500 * time.Millisecond

// isCtxError reports whether err (or any wrapped cause) is a context error.
func isCtxError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// queryVector issues an instant Prometheus query and returns the result as a
// model.Vector. It retries up to queryAttempts times on transient errors,
// backing off exponentially, but stops immediately on context errors.
// A non-vector result type is returned as an error.
func (c *Client) queryVector(ctx context.Context, query string) (model.Vector, error) {
	var val model.Value
	for attempt := 0; attempt < queryAttempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		var queryErr error
		val, _, queryErr = c.api.Query(ctx, query, time.Time{})
		if queryErr == nil {
			break
		}
		if isCtxError(queryErr) {
			return nil, queryErr
		}
		// Transient error: retry if attempts remain, otherwise return it.
		if attempt == queryAttempts-1 {
			return nil, queryErr
		}
		// Interruptible backoff: base * 2^attempt.
		delay := queryBase
		for i := 0; i < attempt; i++ {
			delay *= 2
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	vec, ok := val.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("promclient: unexpected query result type %T (want vector)", val)
	}
	return vec, nil
}

// HarvestInterfaces queries Prometheus for the set of known interfaces and
// returns one IfaceRow per series. skipped counts series whose ifIndex label
// was present but non-numeric (those rows are not appended). An absent ifIndex
// label is allowed and yields IfIndex=0. An empty vector (zero series) is not
// an error. A non-vector result type (matrix, scalar, string) is an error.
//
// The underlying Query call is retried on transient errors (not on context
// cancellation or deadline).
func (c *Client) HarvestInterfaces(ctx context.Context) (rows []IfaceRow, skipped int, err error) {
	vec, err := c.queryVector(ctx, harvestQuery)
	if err != nil {
		return nil, 0, err
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

const capSpeedQuery = `max by(instance, ifIndex)(ifHighSpeed)`
const capStatusQuery = `max by(instance, ifIndex)(ifOperStatus)`

// capKey is a composite map key for (instance, ifIndex) used during the
// outer-join merge in CapacityStatus.
type capKey struct {
	instance string
	ifIndex  int
}

// CapacityStatus queries Prometheus for interface capacity (ifHighSpeed) and
// operational status (ifOperStatus) and returns one CapRow per interface.
// The result is an outer join: an interface present in ifHighSpeed but absent
// from ifOperStatus still returns a CapRow with OperStatus==0, and vice-versa.
// Rows with a non-numeric ifIndex label are skipped (counted in skipped).
// The returned slice is always non-nil and sorted by (Instance, IfIndex).
func (c *Client) CapacityStatus(ctx context.Context) (rows []CapRow, skipped int, err error) {
	speedVec, err := c.queryVector(ctx, capSpeedQuery)
	if err != nil {
		return nil, 0, err
	}
	statusVec, err := c.queryVector(ctx, capStatusQuery)
	if err != nil {
		return nil, 0, err
	}

	merged := make(map[capKey]*CapRow)

	for _, s := range speedVec {
		idxStr := string(s.Metric["ifIndex"])
		if idxStr == "" {
			// absent ifIndex treated as 0, consistent with HarvestInterfaces
			idxStr = "0"
		}
		idx, perr := strconv.Atoi(idxStr)
		if perr != nil {
			skipped++
			continue
		}
		k := capKey{instance: string(s.Metric["instance"]), ifIndex: idx}
		if _, exists := merged[k]; !exists {
			merged[k] = &CapRow{Instance: k.instance, IfIndex: idx}
		}
		merged[k].SpeedMbps = float64(s.Value)
	}

	for _, s := range statusVec {
		idxStr := string(s.Metric["ifIndex"])
		if idxStr == "" {
			idxStr = "0"
		}
		idx, perr := strconv.Atoi(idxStr)
		if perr != nil {
			skipped++
			continue
		}
		k := capKey{instance: string(s.Metric["instance"]), ifIndex: idx}
		if _, exists := merged[k]; !exists {
			merged[k] = &CapRow{Instance: k.instance, IfIndex: idx}
		}
		merged[k].OperStatus = float64(s.Value)
	}

	out := make([]CapRow, 0, len(merged))
	for _, r := range merged {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Instance != out[j].Instance {
			return out[i].Instance < out[j].Instance
		}
		return out[i].IfIndex < out[j].IfIndex
	})
	return out, skipped, nil
}

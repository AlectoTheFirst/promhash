// Package promclient provides a thin client for querying a Prometheus
// server to harvest network interface metadata from SNMP-derived metrics.
package promclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"time"

	promapi "github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

// IfaceRow holds the identifying labels for a single network interface as
// reported by a Prometheus target. The string fields correspond to the
// instance and SNMP ifTable labels (ifName, ifDescr, ifAlias), while IfIndex
// is the numeric SNMP ifIndex. Device is the value of the configured device
// label (see HarvestInterfaces) — the human device name carried on the target
// (e.g. a hostname label from file_sd target files); empty when the label is
// not configured or absent from the series.
type IfaceRow struct {
	Instance, Device, IfName, IfDescr, IfAlias string
	IfIndex                                    int
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

// labelNameRe is the Prometheus label-name grammar. The device label is
// interpolated into the harvest query, so it must be validated against this
// (not just for correctness — an arbitrary string would be query injection).
var labelNameRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// harvestQuery builds the interface-harvest query, optionally grouping by an
// additional device label. deviceLabel must be pre-validated by the caller.
func harvestQuery(deviceLabel string) string {
	labels := "instance, ifIndex, ifName, ifDescr, ifAlias"
	if deviceLabel != "" {
		labels += ", " + deviceLabel
	}
	return "group by (" + labels + ") (ifHCInOctets)"
}

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
// returns one IfaceRow per series. deviceLabel, when non-empty, names the
// series label that carries the human device name (e.g. "hostname" stamped by
// file_sd target files); it is added to the harvest grouping and surfaced as
// IfaceRow.Device. An invalid label name is an error (it would otherwise be
// query injection). skipped counts series whose ifIndex label was present but
// non-numeric (those rows are not appended). An absent ifIndex label is
// allowed and yields IfIndex=0. An empty vector (zero series) is not an
// error. A non-vector result type (matrix, scalar, string) is an error.
//
// The underlying Query call is retried on transient errors (not on context
// cancellation or deadline).
func (c *Client) HarvestInterfaces(ctx context.Context, deviceLabel string) (rows []IfaceRow, skipped int, err error) {
	if deviceLabel != "" && !labelNameRe.MatchString(deviceLabel) {
		return nil, 0, fmt.Errorf("promclient: invalid device label name %q", deviceLabel)
	}
	vec, err := c.queryVector(ctx, harvestQuery(deviceLabel))
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
		var dev string
		if deviceLabel != "" {
			dev = string(s.Metric[model.LabelName(deviceLabel)])
		}
		out = append(out, IfaceRow{
			Instance: string(s.Metric["instance"]), Device: dev, IfName: string(s.Metric["ifName"]),
			IfDescr: string(s.Metric["ifDescr"]), IfAlias: string(s.Metric["ifAlias"]), IfIndex: idx})
	}
	return out, skipped, nil
}

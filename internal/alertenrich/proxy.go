package alertenrich

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/AlectoTheFirst/promhash/internal/graph"
)

// ProxyConfig configures a Proxy. Client, Upstreams, LabelMap and Render are
// required; the rest have sane zero-value fallbacks applied by NewProxy.
type ProxyConfig struct {
	Client         ImpactClient
	Upstreams      []string              // upstream Alertmanager base URLs
	LabelMap       LabelMap              // which alert labels carry device/iface identity
	Render         RenderCfg             // how impact becomes labels/annotations
	Timeout        time.Duration         // per-alert lookup budget (default 2s)
	EnrichResolved bool                  // also add impact annotation to resolved alerts
	HTTPClient     *http.Client          // forwarding client (default 10s timeout)
	Registerer     prometheus.Registerer // self-metrics target (default DefaultRegisterer)
	Now            func() time.Time      // clock for resolved detection (default time.Now)
}

// Proxy is the HTTP handler that enriches an Alertmanager v2 alert batch and
// forwards it upstream. It is stateless and safe for concurrent use.
type Proxy struct {
	client         ImpactClient
	upstreams      []string
	labelMap       LabelMap
	render         RenderCfg
	timeout        time.Duration
	enrichResolved bool
	hc             *http.Client
	now            func() time.Time
	m              *metrics
	cache          *lookupCache
}

// errAllUpstreams indicates no upstream Alertmanager accepted the batch.
var errAllUpstreams = errors.New("alertenrich: all upstream alertmanagers failed")

// NewProxy builds a Proxy from cfg, applying defaults and registering metrics.
func NewProxy(cfg ProxyConfig) *Proxy {
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Registerer == nil {
		cfg.Registerer = prometheus.DefaultRegisterer
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Proxy{
		client:         cfg.Client,
		upstreams:      cfg.Upstreams,
		labelMap:       cfg.LabelMap,
		render:         cfg.Render,
		timeout:        cfg.Timeout,
		enrichResolved: cfg.EnrichResolved,
		hc:             cfg.HTTPClient,
		now:            cfg.Now,
		m:              newMetrics(cfg.Registerer),
		cache:          newLookupCache(cfg.Now),
	}
}

// ServeHTTP handles POST /api/v2/alerts: parse, enrich each alert (fail-open),
// then forward the batch to all upstream Alertmanagers.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	alerts, err := parseAlerts(body)
	if err != nil {
		http.Error(w, "invalid alert payload", http.StatusBadRequest)
		return
	}
	p.m.received.Add(float64(len(alerts)))

	for _, a := range alerts {
		p.enrichOne(r.Context(), a)
	}

	out, err := marshalAlerts(alerts)
	if err != nil {
		// Should not happen; fall back to forwarding the original bytes.
		out = body
	}
	if err := p.forward(r.Context(), out); err != nil {
		log.Printf("alertenrich: forward: %v", err)
		http.Error(w, "upstream alertmanager unavailable", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// enrichOne mutates a in place when impact is found; on any failure it leaves a
// unchanged and records the reason. Never returns an error (fail-open).
func (p *Proxy) enrichOne(ctx context.Context, a rawAlert) {
	labels, err := labelsOf(a)
	if err != nil {
		p.m.passthrough.WithLabelValues("error").Inc()
		return
	}
	key, ok := Correlate(labels, p.labelMap)
	if !ok {
		p.m.passthrough.WithLabelValues("no_key").Inc()
		return
	}

	var at time.Time
	if s := startsAtOf(a); s != "" {
		if ts, perr := time.Parse(time.RFC3339, s); perr == nil {
			at = ts
		}
	}

	lctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	start := p.now()
	rows, lerr := p.lookup(lctx, key, at)
	p.m.lookup.Observe(p.now().Sub(start).Seconds())
	if lerr != nil {
		// A failed lookup falls back to the cached result of this alert's
		// earlier successful lookup (same correlation key, same startsAt), so
		// a resolved alert keeps the labels its firing notification got and
		// the fingerprints stay identical. No cache entry → plain fail-open.
		cached, hit := p.cache.get(key, at)
		if !hit {
			reason := "error"
			if errors.Is(lerr, context.DeadlineExceeded) {
				reason = "timeout"
			}
			p.m.passthrough.WithLabelValues(reason).Inc()
			return
		}
		rows = cached
	}
	if len(rows) == 0 {
		p.m.passthrough.WithLabelValues("no_match").Inc()
		return
	}
	if lerr == nil {
		p.cache.put(key, at, rows)
	}

	outLabels, outAnnotations := Render(rows, p.render)

	// Labels are applied to BOTH firing and resolved alerts so the resolved
	// alert keeps the same fingerprint and clears correctly (design §8).
	if outLabels != nil {
		for k, v := range outLabels {
			labels[k] = v
		}
		if err := setLabels(a, labels); err != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
	}

	// The impact annotation is added to firing alerts always, and to resolved
	// alerts only when EnrichResolved is set.
	if outAnnotations != nil && (!p.isResolved(a) || p.enrichResolved) {
		annotations, aerr := annotationsOf(a)
		if aerr != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
		for k, v := range outAnnotations {
			annotations[k] = v
		}
		if err := setAnnotations(a, annotations); err != nil {
			p.m.passthrough.WithLabelValues("error").Inc()
			return
		}
	}

	p.m.enriched.Inc()
}

// lookup dispatches to the exact or name client method based on the key kind.
func (p *Proxy) lookup(ctx context.Context, key Key, at time.Time) ([]graph.ImpactRow, error) {
	switch key.Kind {
	case KeyExact:
		return p.client.ImpactByInstanceIndex(ctx, key.Instance, key.IfIndex, at)
	case KeyName:
		return p.client.ImpactByName(ctx, key.Device, key.IfName, at)
	default:
		return nil, nil
	}
}

// isResolved reports whether the alert's endsAt is in the past relative to the
// proxy clock (Alertmanager v2 marks an alert resolved by an elapsed endsAt).
func (p *Proxy) isResolved(a rawAlert) bool {
	s := endsAtOf(a)
	if s == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return false
	}
	return ts.Before(p.now())
}

// forward POSTs body to every upstream Alertmanager's /api/v2/alerts. It returns
// nil if at least one upstream accepts (2xx); errAllUpstreams otherwise.
func (p *Proxy) forward(ctx context.Context, body []byte) error {
	var ok bool
	for _, base := range p.upstreams {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v2/alerts", bytes.NewReader(body))
		if err != nil {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := p.hc.Do(req)
		if err != nil {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			p.m.forwardErr.WithLabelValues(base).Inc()
			continue
		}
		ok = true
	}
	if !ok {
		return errAllUpstreams
	}
	return nil
}

// metrics holds the proxy's self-observability counters/histogram.
type metrics struct {
	received    prometheus.Counter
	enriched    prometheus.Counter
	passthrough *prometheus.CounterVec
	forwardErr  *prometheus.CounterVec
	lookup      prometheus.Histogram
}

// newMetrics constructs and registers the metric set on reg. Using a fresh
// prometheus.NewRegistry() in tests avoids duplicate-registration panics.
func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		received: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_received_total", Help: "Alerts received from Prometheus."}),
		enriched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_enriched_total", Help: "Alerts enriched with impact."}),
		passthrough: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_alerts_passthrough_total", Help: "Alerts forwarded un-enriched, by reason."},
			[]string{"reason"}),
		forwardErr: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "promhash_alert_proxy_forward_errors_total", Help: "Upstream forward failures, by upstream."},
			[]string{"upstream"}),
		lookup: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "promhash_alert_proxy_lookup_seconds", Help: "Impact lookup latency.",
			Buckets: prometheus.DefBuckets}),
	}
	reg.MustRegister(m.received, m.enriched, m.passthrough, m.forwardErr, m.lookup)
	return m
}

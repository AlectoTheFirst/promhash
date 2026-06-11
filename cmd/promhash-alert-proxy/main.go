// Command promhash-alert-proxy enriches Alertmanager v2 alerts in flight with
// graph blast-radius data and forwards them to the real Alertmanager. Point
// Prometheus' alertmanagers config at this proxy. It fails open: any enrichment
// error forwards the alert unchanged.
//
// The promhash-api bearer token is read from the PROMHASH_API_TOKEN environment
// variable (never a flag — secrets must not appear in process listings).
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/AlectoTheFirst/promhash/internal/alertenrich"
	"github.com/AlectoTheFirst/promhash/internal/httpx"
)

// opts holds the raw flag values before assembly into a ProxyConfig.
type opts struct {
	listen         string
	upstreams      string
	apiBase        string
	apiToken       string
	deviceLabel    string
	ifIndexLabel   string
	ifNameLabel    string
	timeout        time.Duration
	prefix         string
	enrichLabels   bool
	enrichResolved bool
	tlsCert        string
	tlsKey         string
}

func main() {
	if err := run(); err != nil {
		log.Printf("promhash-alert-proxy: %v", err)
		os.Exit(1)
	}
}

// parseUpstreams splits a comma-separated list into trimmed, non-empty URLs.
func parseUpstreams(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// buildConfig assembles a ProxyConfig from opts, wiring the C7 API client with a
// lookup-bounded HTTP client and applying default label names.
func buildConfig(o opts) alertenrich.ProxyConfig {
	if o.deviceLabel == "" {
		o.deviceLabel = "instance"
	}
	if o.ifIndexLabel == "" {
		o.ifIndexLabel = "ifIndex"
	}
	if o.ifNameLabel == "" {
		o.ifNameLabel = "ifName"
	}
	if o.prefix == "" {
		o.prefix = "promhash_"
	}
	if o.timeout == 0 {
		o.timeout = 2 * time.Second
	}
	apiHTTP := &http.Client{Timeout: o.timeout}
	return alertenrich.ProxyConfig{
		Client:         alertenrich.NewAPIClient(o.apiBase, apiHTTP, o.apiToken),
		Upstreams:      parseUpstreams(o.upstreams),
		LabelMap:       alertenrich.LabelMap{DeviceLabel: o.deviceLabel, IfIndexLabel: o.ifIndexLabel, IfNameLabel: o.ifNameLabel},
		Render:         alertenrich.RenderCfg{Prefix: o.prefix, EnrichLabels: o.enrichLabels},
		Timeout:        o.timeout,
		EnrichResolved: o.enrichResolved,
		Registerer:     prometheus.DefaultRegisterer,
	}
}

func run() error {
	var o opts
	flag.StringVar(&o.listen, "listen", "127.0.0.1:9094", "alert intake + /metrics listen address")
	flag.StringVar(&o.upstreams, "upstreams", "", "comma-separated upstream Alertmanager base URLs")
	flag.StringVar(&o.apiBase, "promhash-api", "http://127.0.0.1:8080", "promhash-api base URL")
	flag.StringVar(&o.deviceLabel, "device-label", "instance", "alert label carrying the device/target")
	flag.StringVar(&o.ifIndexLabel, "ifindex-label", "ifIndex", "alert label carrying ifIndex")
	flag.StringVar(&o.ifNameLabel, "ifname-label", "ifName", "alert label for the name fallback")
	flag.DurationVar(&o.timeout, "lookup-timeout", 2*time.Second, "per-alert impact lookup timeout")
	flag.StringVar(&o.prefix, "label-prefix", "promhash_", "prefix for stamped labels/annotations")
	flag.BoolVar(&o.enrichLabels, "enrich-labels", true, "stamp bounded routing labels (false => annotations only)")
	flag.BoolVar(&o.enrichResolved, "enrich-resolved", true, "add impact annotation to resolved alerts too")
	flag.StringVar(&o.tlsCert, "tls-cert", "", "TLS certificate file; with -tls-key, serve HTTPS (TLS 1.2+)")
	flag.StringVar(&o.tlsKey, "tls-key", "", "TLS private-key file; must be set together with -tls-cert")
	flag.Parse()

	if err := httpx.ValidateTLSFlags(o.tlsCert, o.tlsKey); err != nil {
		return err
	}

	// The API bearer token comes from the environment, mirroring the other
	// tools' secrets-via-env contract.
	o.apiToken = os.Getenv("PROMHASH_API_TOKEN")

	if len(parseUpstreams(o.upstreams)) == 0 {
		return errors.New("at least one -upstreams Alertmanager URL is required")
	}

	proxy := alertenrich.NewProxy(buildConfig(o))

	mux := http.NewServeMux()
	mux.Handle("POST /api/v2/alerts", proxy)
	mux.Handle("GET /metrics", promhttp.Handler())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              o.listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		scheme := "http"
		if o.tlsCert != "" {
			scheme = "https"
		}
		log.Printf("promhash-alert-proxy listening on %s (%s) -> %v", o.listen, scheme, parseUpstreams(o.upstreams))
		if err := httpx.ListenAndServe(srv, o.tlsCert, o.tlsKey); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	log.Print("promhash-alert-proxy shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// Command promhash-demo-exporter serves synthetic SNMP-shaped interface
// metrics for the docker-compose demo. It emulates a tiny three-router
// topology with moving octet counters, one near-saturated core trunk, and one
// interface that flaps periodically (so the demo's InterfaceDown alert fires
// and the enrichment proxy has something to enrich).
//
// One process serves every device: Prometheus scrapes it once per device with
// ?device=<hostname> (the demo scrape config sets the param from the target's
// hostname label), mirroring the snmp_exporter target pattern. All values are
// computed from wall-clock time, so the exporter is stateless and counters
// are monotonic across scrapes (a restart resets them; rate() handles that).
//
// Demo only — never deploy against anything real.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

// iface is one synthetic interface: identity labels, capacity, and per-second
// octet rates. flap marks the interface that goes oper-down for flapDown
// seconds at the start of every flapPeriod.
type iface struct {
	index     int
	name      string
	descr     string
	alias     string
	speedMbps int
	inRate    float64 // bytes/s
	outRate   float64 // bytes/s
	flap      bool
}

const (
	flapPeriod = 600 // seconds; one flap cycle
	flapDown   = 90  // seconds down at the start of each cycle
)

// topology maps device hostname → its interfaces. The demo declarations under
// demo/declared/ reference exactly these (device, ifName) pairs.
var topology = map[string][]iface{
	"rtr-edge-1": {
		{index: 11, name: "Te0/0/1", descr: "TenGigE0/0/1", alias: "uplink-core", speedMbps: 10000, inRate: 28e6, outRate: 31e6},
		{index: 12, name: "Gi0/0/0", descr: "GigabitEthernet0/0/0", alias: "mgmt", speedMbps: 1000, inRate: 4e3, outRate: 6e3},
	},
	"rtr-core-1": {
		// Near-saturated 1G trunk: ~110 MB/s of 125 MB/s ≈ 88% utilization.
		{index: 21, name: "Te0/1/0", descr: "TenGigE0/1/0", alias: "trunk-dc", speedMbps: 1000, inRate: 96e6, outRate: 110e6},
		// The flapping link: checkout's path crosses it.
		{index: 22, name: "Te0/1/1", descr: "TenGigE0/1/1", alias: "trunk-dc-alt", speedMbps: 10000, inRate: 12e6, outRate: 15e6, flap: true},
	},
	"rtr-dc-1": {
		{index: 31, name: "Te1/0/0", descr: "TenGigE1/0/0", alias: "dc-ingress", speedMbps: 10000, inRate: 41e6, outRate: 22e6},
	},
}

// flapState returns the ifOperStatus value (1 up, 2 down) and the cumulative
// error count for a flapping interface at unix time now. Errors accrue at
// 1/s while the interface is down, so the counter is monotonic.
func flapState(now int64) (oper int, errors int64) {
	cycles := now / flapPeriod
	into := now % flapPeriod
	oper = 1
	if into < flapDown {
		oper = 2
	}
	partial := into
	if partial > flapDown {
		partial = flapDown
	}
	return oper, cycles*flapDown + partial
}

// writeMetrics renders the exposition text for one device at unix time now,
// with counters measured since epoch.
func writeMetrics(w http.ResponseWriter, device string, epoch, now int64) {
	ifs, ok := topology[device]
	if !ok {
		http.Error(w, "unknown device", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	elapsed := float64(now - epoch)
	for _, i := range ifs {
		l := fmt.Sprintf(`ifIndex="%d",ifName="%s",ifDescr="%s",ifAlias="%s"`, i.index, i.name, i.descr, i.alias)
		oper, errCount := 1, int64(0)
		if i.flap {
			oper, errCount = flapState(now)
		}
		fmt.Fprintf(w, "ifHCInOctets{%s} %d\n", l, int64(i.inRate*elapsed))
		fmt.Fprintf(w, "ifHCOutOctets{%s} %d\n", l, int64(i.outRate*elapsed))
		fmt.Fprintf(w, "ifHighSpeed{%s} %d\n", l, i.speedMbps)
		fmt.Fprintf(w, "ifOperStatus{%s} %d\n", l, oper)
		fmt.Fprintf(w, "ifInErrors{%s} %d\n", l, errCount)
		fmt.Fprintf(w, "ifOutErrors{%s} 0\n", l)
		fmt.Fprintf(w, "ifInDiscards{%s} 0\n", l)
		fmt.Fprintf(w, "ifOutDiscards{%s} 0\n", l)
	}
}

func main() {
	var listen string
	flag.StringVar(&listen, "listen", ":9116", "metrics listen address")
	flag.Parse()

	epoch := time.Now().Unix()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		writeMetrics(w, r.URL.Query().Get("device"), epoch, time.Now().Unix())
	})

	log.Printf("promhash-demo-exporter listening on %s (devices: rtr-edge-1, rtr-core-1, rtr-dc-1)", listen)
	srv := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}

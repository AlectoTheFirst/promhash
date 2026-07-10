package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/AlectoTheFirst/promhash/internal/promclient"
)

var buildNow = time.Unix(1700000000, 0).UTC()

func TestBuildIfacesDevicePrecedence(t *testing.T) {
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-labeled", IfName: "Te0/0/1", IfIndex: 1},
		{Instance: "10.0.0.2:9116", IfName: "Te0/0/2", IfIndex: 2}, // nautobot map
		{Instance: "10.0.0.3:9116", IfName: "Te0/0/3", IfIndex: 3}, // instance fallback
	}
	devMap := map[string]string{"10.0.0.2:9116": "rtr-nautobot"}
	b := buildIfaces(rows, devMap, "cisco", buildNow)
	if b.skipped != 0 {
		t.Fatalf("skipped=%d, warnings=%v; want 0 skips", b.skipped, b.warnings)
	}
	if len(b.ifaces) != 3 {
		t.Fatalf("want 3 ifaces, got %d", len(b.ifaces))
	}
	if b.ifaces[0].Device != "rtr-labeled" {
		t.Errorf("row 0: device label must win, got %q", b.ifaces[0].Device)
	}
	if b.ifaces[1].Device != "rtr-nautobot" {
		t.Errorf("row 1: nautobot map must win over instance, got %q", b.ifaces[1].Device)
	}
	// The critical fix: host:port instance fallback strips the port instead of
	// being rejected by SafeKey for containing ':'.
	if b.ifaces[2].Device != "10.0.0.3" {
		t.Errorf("row 2: instance fallback must strip the port, got %q", b.ifaces[2].Device)
	}
}

func TestBuildIfacesInstanceFallbackWithoutPort(t *testing.T) {
	rows := []promclient.IfaceRow{{Instance: "rtr-bare", IfName: "Te0/0/1", IfIndex: 1}}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if len(b.ifaces) != 1 || b.ifaces[0].Device != "rtr-bare" {
		t.Fatalf("portless instance must pass through unchanged, got %+v (skipped=%d)", b.ifaces, b.skipped)
	}
}

func TestBuildIfacesSkipsAndWarns(t *testing.T) {
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-1"}, // empty ifName AND ifDescr
	}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if b.skipped != 1 || len(b.ifaces) != 0 {
		t.Fatalf("want 1 skip 0 ifaces, got skipped=%d ifaces=%d", b.skipped, len(b.ifaces))
	}
	if len(b.warnings) != 1 || !strings.Contains(b.warnings[0], "empty canonical name") {
		t.Fatalf("want empty-canonical-name warning, got %v", b.warnings)
	}
}

func TestBuildIfacesCollisionWarning(t *testing.T) {
	// Two exporters scrape the same device name: identical (device, ifName)
	// phash, different instance. Last write wins, but it must warn.
	rows := []promclient.IfaceRow{
		{Instance: "10.0.0.1:9116", Device: "rtr-dup", IfName: "Te0/0/1", IfIndex: 1},
		{Instance: "10.0.0.9:9116", Device: "rtr-dup", IfName: "Te0/0/1", IfIndex: 1},
	}
	b := buildIfaces(rows, nil, "cisco", buildNow)
	if b.skipped != 0 {
		t.Fatalf("collisions are not skips: skipped=%d", b.skipped)
	}
	found := false
	for _, w := range b.warnings {
		if strings.Contains(w, "identity collision") &&
			strings.Contains(w, "10.0.0.1:9116") && strings.Contains(w, "10.0.0.9:9116") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want identity-collision warning naming both instances, got %v", b.warnings)
	}
}

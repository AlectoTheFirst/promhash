package catalog

import (
	"errors"
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

func errorsAs(err error, target any) bool {
	return errors.As(err, target)
}

func cat() []graph.Iface {
	return []graph.Iface{
		{PHash: "interface:1", Device: "rtr-core-1", IfName: "tengige0/1/2",
			MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink-ledger-dc", Vendor: "cisco"},
		{PHash: "interface:2", Device: "rtr-core-1", IfName: "tengige0/1/3",
			MetricIfName: "Te0/1/3", IfDescr: "TenGigE0/1/3", IfAlias: "uplink-auth-dc", Vendor: "cisco"},
	}
}

func TestResolveByName(t *testing.T) {
	r := NewResolver(cat())
	got, err := r.Resolve("rtr-core-1", "Te0/1/2")
	if err != nil {
		t.Fatal(err)
	}
	if got.PHash != "interface:1" {
		t.Fatalf("got %s", got.PHash)
	}
}

func TestResolveByAlias(t *testing.T) {
	r := NewResolver(cat())
	got, err := r.Resolve("rtr-core-1", "uplink-ledger-dc")
	if err != nil {
		t.Fatal(err)
	}
	if got.PHash != "interface:1" {
		t.Fatalf("got %s", got.PHash)
	}
}

func TestResolveNoMatchFailsLoud(t *testing.T) {
	r := NewResolver(cat())
	_, err := r.Resolve("rtr-core-1", "Te9/9/9")
	var nm *NoMatchError
	if !errorsAs(err, &nm) {
		t.Fatalf("want NoMatchError, got %v", err)
	}
	if len(nm.Suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
}

// TestResolveDeviceCaseVariants exercises the F-id fix: Sync now stores the
// normalized (lower+trimmed) device, and Resolve normalizes its lookup key, so
// all case/whitespace variants of a device name must resolve to the same node.
func TestResolveDeviceCaseVariants(t *testing.T) {
	// Build a catalog with a node stored under the normalized key "rtr1",
	// matching what Sync would write after phash.NormDevice.
	ifaces := []graph.Iface{
		{PHash: "interface:abc123", Device: "rtr1", IfName: "tengige0/1/2",
			MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", Vendor: "cisco"},
	}
	r := NewResolver(ifaces)

	variants := []string{"rtr1", "Rtr1", "RTR1", "  rtr1  ", "  RTR1  "}
	for _, dev := range variants {
		got, err := r.Resolve(dev, "Te0/1/2")
		if err != nil {
			t.Errorf("Resolve(%q, ...) unexpected error: %v", dev, err)
			continue
		}
		if got.PHash != "interface:abc123" {
			t.Errorf("Resolve(%q, ...) got PHash %q; want interface:abc123", dev, got.PHash)
		}
	}
}

// TestResolveDeviceCaseVariantsAllSamePHash asserts that the PHash returned for
// every device-name variant is identical (same node, not just same struct fields).
func TestResolveDeviceCaseVariantsAllSamePHash(t *testing.T) {
	ifaces := []graph.Iface{
		{PHash: "interface:deadbeef", Device: "rtr1", IfName: "tengige0/1/2",
			MetricIfName: "Te0/1/2", Vendor: "cisco"},
	}
	r := NewResolver(ifaces)

	want := "interface:deadbeef"
	for _, dev := range []string{"rtr1", "Rtr1", "  RTR1  "} {
		got, err := r.Resolve(dev, "Te0/1/2")
		if err != nil {
			t.Fatalf("Resolve(%q): %v", dev, err)
		}
		if got.PHash != want {
			t.Errorf("Resolve(%q): PHash = %q; want %q", dev, got.PHash, want)
		}
	}
}

// TestSafeKeyGuard verifies that phash.SafeKey rejects ':' and control chars.
// These are the values that Sync would skip rather than store, protecting the
// ':'-delimited identity key scheme.
func TestSafeKeyGuard(t *testing.T) {
	if err := phash.SafeKey("device", "a:b"); err == nil {
		t.Error("expected error for ':' in device name, got nil")
	}
	if err := phash.SafeKey("device", "a\x00b"); err == nil {
		t.Error("expected error for control char in device name, got nil")
	}
	if err := phash.SafeKey("device", "valid-device"); err != nil {
		t.Errorf("unexpected error for valid device: %v", err)
	}
}

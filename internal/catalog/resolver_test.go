package catalog

import (
	"errors"
	"strings"
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

// TestResolveEmptyRefReturnsNoMatch verifies that Resolve with an empty (or
// whitespace-only) ref always returns *NoMatchError and never binds to a
// degenerate interface node whose IfName or IfAlias happens to be empty.
func TestResolveEmptyRefReturnsNoMatch(t *testing.T) {
	ifaces := []graph.Iface{
		// Degenerate interface: IfName is empty (as would result from a skipped
		// sync row that somehow made it in before the F3 guard).
		{PHash: "interface:degenerate", Device: "rtr1", IfName: "",
			MetricIfName: "", IfDescr: "", IfAlias: "", Vendor: "cisco"},
		// Normal interface on the same device.
		{PHash: "interface:good", Device: "rtr1", IfName: "tengige0/1/2",
			MetricIfName: "Te0/1/2", IfDescr: "TenGigE0/1/2", IfAlias: "uplink", Vendor: "cisco"},
	}
	res := NewResolver(ifaces)

	for _, ref := range []string{"", "   ", "\t"} {
		_, err := res.Resolve("rtr1", ref)
		var nm *NoMatchError
		if !errors.As(err, &nm) {
			t.Errorf("Resolve(%q, %q) want *NoMatchError, got %v", "rtr1", ref, err)
		}
	}
}

// TestResolveNonEmptyRefDoesNotMatchEmptyStoredAlias verifies that a non-empty
// ref does not match an interface whose IfAlias or MetricIfName is empty, and
// conversely that it still resolves to the correct interface when the fields are
// populated.
func TestResolveNonEmptyRefDoesNotMatchEmptyStoredAlias(t *testing.T) {
	ifaces := []graph.Iface{
		// Interface with empty IfAlias and MetricIfName — must NOT match a
		// non-empty ref solely because the stored field is empty.
		{PHash: "interface:sparse", Device: "rtr1", IfName: "gi0/1",
			MetricIfName: "", IfDescr: "GigabitEthernet0/1", IfAlias: "", Vendor: "cisco"},
		// Interface with populated fields.
		{PHash: "interface:full", Device: "rtr1", IfName: "gi0/2",
			MetricIfName: "Gi0/2", IfDescr: "GigabitEthernet0/2", IfAlias: "uplink", Vendor: "cisco"},
	}
	res := NewResolver(ifaces)

	// A ref that would equal the empty stored IfAlias must NOT match.
	_, err := res.Resolve("rtr1", "Gi0/1")
	if err != nil {
		// Gi0/1 should resolve via IfName "gi0/1" (canonical match), so err
		// would be nil — this branch is here to show intent.
		t.Logf("Resolve Gi0/1 via IfName: %v (ok if nil)", err)
	}

	// A ref that cannot match anything should return NoMatchError, not
	// accidentally bind to the interface with empty MetricIfName.
	_, err = res.Resolve("rtr1", "no-such-if")
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Errorf("Resolve(%q, %q) want *NoMatchError, got %v", "rtr1", "no-such-if", err)
	}

	// Normal resolve still works.
	got, err := res.Resolve("rtr1", "uplink")
	if err != nil {
		t.Fatalf("Resolve(rtr1, uplink): %v", err)
	}
	if got.PHash != "interface:full" {
		t.Errorf("Resolve(rtr1, uplink): got PHash %q; want interface:full", got.PHash)
	}
}

// TestResolveAristaEtAbbrev verifies that "Et1" resolves to the same interface
// as "Ethernet1" when the catalog stores MetricIfName "Ethernet1" (i.e. the
// et->ethernet abbreviation actually drives interface selection).
func TestResolveAristaEtAbbrev(t *testing.T) {
	ifaces := []graph.Iface{
		{PHash: "interface:arista1", Device: "sw-arista-1", Vendor: "arista",
			IfName: "ethernet1", MetricIfName: "Ethernet1", IfDescr: "Ethernet1"},
	}
	r := NewResolver(ifaces)
	for _, ref := range []string{"Et1", "Eth1", "Ethernet1"} {
		got, err := r.Resolve("sw-arista-1", ref)
		if err != nil {
			t.Errorf("Resolve(%q, %q) unexpected error: %v", "sw-arista-1", ref, err)
			continue
		}
		if got.PHash != "interface:arista1" {
			t.Errorf("Resolve(%q, %q) PHash = %q; want interface:arista1", "sw-arista-1", ref, got.PHash)
		}
	}
}

// TestNoMatchErrorUnknownDevice verifies that resolving an unknown device
// produces a *NoMatchError whose Error() message does NOT contain the dangling
// "did you mean:" clause, and instead communicates that no interfaces are known.
func TestNoMatchErrorUnknownDevice(t *testing.T) {
	r := NewResolver(cat())
	_, err := r.Resolve("no-such-device", "Te0/1")
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("want *NoMatchError, got %v", err)
	}
	msg := nm.Error()
	if strings.Contains(msg, "did you mean:") {
		t.Errorf("unknown-device error should not contain 'did you mean:'; got: %q", msg)
	}
	if !strings.Contains(msg, "no interfaces known") {
		t.Errorf("unknown-device error should mention 'no interfaces known'; got: %q", msg)
	}
}

// TestNoMatchErrorKnownDeviceBadRef verifies that resolving a known device with
// a nonexistent ref yields a *NoMatchError with non-empty Suggestions and an
// Error() message that contains "did you mean".
func TestNoMatchErrorKnownDeviceBadRef(t *testing.T) {
	r := NewResolver(cat())
	_, err := r.Resolve("rtr-core-1", "Zz9/9/9")
	var nm *NoMatchError
	if !errors.As(err, &nm) {
		t.Fatalf("want *NoMatchError, got %v", err)
	}
	if len(nm.Suggestions) == 0 {
		t.Fatal("expected non-empty Suggestions for known device")
	}
	if !strings.Contains(nm.Error(), "did you mean") {
		t.Errorf("known-device no-match error should contain 'did you mean'; got: %q", nm.Error())
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

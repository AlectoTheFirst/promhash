package catalog

import (
	"testing"

	"github.com/AlectoTheFirst/promhash/internal/graph"
	"github.com/AlectoTheFirst/promhash/internal/phash"
)

func TestCanonicalIfName(t *testing.T) {
	cases := map[[2]string]string{
		{"cisco", "Gi0/3"}:              "gigabitethernet0/3",
		{"cisco", "GigabitEthernet0/3"}: "gigabitethernet0/3",
		{"cisco", "Te0/1/2"}:            "tengige0/1/2",
		{"juniper", "ge-0/0/3"}:         "ge-0/0/3",
		{"arista", "Eth3"}:              "ethernet3",
		{"", "  Te0/1/2 "}:              "tengige0/1/2",
	}
	for in, want := range cases {
		if got := CanonicalIfName(in[0], in[1]); got != want {
			t.Errorf("CanonicalIfName(%q,%q)=%q want %q", in[0], in[1], got, want)
		}
	}
}

// TestCanonAristaEtFoldsToEthernet verifies that the Arista short form "Et"
// folds to the same canonical name as "Eth" and "Ethernet".
func TestCanonAristaEtFoldsToEthernet(t *testing.T) {
	want := "ethernet1"
	for _, raw := range []string{"Et1", "Eth1", "Ethernet1"} {
		if got := CanonicalIfName("arista", raw); got != want {
			t.Errorf("CanonicalIfName(%q,%q) = %q; want %q", "arista", raw, got, want)
		}
	}
}

// TestCanonDeadSelfMapRemovalSafe verifies that removing the dead self-mapping
// rows from the abbrev table does not change canonical output for a
// representative set of inputs that previously relied on those rows.
func TestCanonDeadSelfMapRemovalSafe(t *testing.T) {
	cases := map[[2]string]string{
		{"cisco", "Te0/1/2"}:            "tengige0/1/2",
		{"cisco", "Gi0/1"}:              "gigabitethernet0/1",
		{"cisco", "Fa0/1"}:              "fastethernet0/1",
		{"cisco", "Hu1/1"}:              "hundredgige1/1",
		{"arista", "Eth1"}:              "ethernet1",
		{"cisco", "TenGigE0/1/2"}:       "tengige0/1/2",
		{"cisco", "GigabitEthernet0/1"}: "gigabitethernet0/1",
		{"cisco", "FastEthernet0/1"}:    "fastethernet0/1",
		{"cisco", "HundredGigE1/1"}:     "hundredgige1/1",
		{"arista", "Ethernet1"}:         "ethernet1",
	}
	for in, want := range cases {
		if got := CanonicalIfName(in[0], in[1]); got != want {
			t.Errorf("CanonicalIfName(%q,%q) = %q; want %q", in[0], in[1], got, want)
		}
	}
}

// --- F-norm behavioral tests (fail before normalizeToken is wired in) ---

// TestCanonInternalASCIIWhitespaceCollapsed verifies that ASCII spaces between
// the prefix and suffix of an interface name are collapsed away, so that
// "Gi 0/3" and "Gi0/3" produce the same canonical name.
func TestCanonInternalASCIIWhitespaceCollapsed(t *testing.T) {
	a := CanonicalIfName("cisco", "Gi 0/3")
	b := CanonicalIfName("cisco", "Gi0/3")
	if a != b {
		t.Errorf("internal ASCII space not collapsed: %q != %q", a, b)
	}
}

// TestCanonNBSPTreatedAsWhitespace verifies that a non-breaking space (U+00A0)
// between the alpha prefix and the numeric suffix is treated as whitespace and
// collapsed, so "Gi 0/3" canonicalises identically to "Gi0/3".
func TestCanonNBSPTreatedAsWhitespace(t *testing.T) {
	a := CanonicalIfName("cisco", "Gi 0/3")
	b := CanonicalIfName("cisco", "Gi0/3")
	if a != b {
		t.Errorf("NBSP not treated as whitespace: %q != %q", a, b)
	}
}

// TestCanonFullWidthFoldsToASCII verifies that full-width Unicode equivalents
// (NFKC compatibility-decompose to ASCII) produce the same canonical name as
// their ASCII counterpart.  The input uses full-width "Ｇｉ" (U+FF27 U+FF49) and
// full-width digits "０" "３" (U+FF10, U+FF13).
func TestCanonFullWidthFoldsToASCII(t *testing.T) {
	fullWidth := "Ｇｉ０/３" // U+FF27 U+FF49 U+FF10 / U+FF13
	ascii := "Gi0/3"
	a := CanonicalIfName("cisco", fullWidth)
	b := CanonicalIfName("cisco", ascii)
	if a != b {
		t.Errorf("full-width did not fold to ASCII: %q != %q", a, b)
	}
}

// TestDeviceNormWhitespaceAndFullWidthSharePHash verifies that the device
// identity path uses the same normalizer as the interface path: building a
// Resolver from an Iface and resolving using whitespace-padded, NBSP, and
// full-width variants of the device name all return the same PHash.
func TestDeviceNormWhitespaceAndFullWidthSharePHash(t *testing.T) {
	const wantPHash = "interface:testdev1"
	ifaces := []graph.Iface{
		{
			PHash:        wantPHash,
			Device:       phash.NormDevice("rtr1"),
			IfName:       CanonicalIfName("cisco", "Gi0/1"),
			MetricIfName: "Gi0/1",
			Vendor:       "cisco",
		},
	}
	r := NewResolver(ifaces)

	variants := []string{
		"rtr1",
		"Rtr1",
		"  rtr1  ",
		"rtr 1",  // NBSP between prefix and suffix
		"rtr  1", // multiple NBSP
	}
	for _, dev := range variants {
		got, err := r.Resolve(dev, "Gi0/1")
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error: %v", dev, err)
			continue
		}
		if got.PHash != wantPHash {
			t.Errorf("Resolve(%q): got PHash %q; want %q", dev, got.PHash, wantPHash)
		}
	}
}

// TestDeviceNormFullWidthSharePHash verifies that full-width Unicode device
// name variants resolve to the same PHash as the ASCII form.
func TestDeviceNormFullWidthSharePHash(t *testing.T) {
	const wantPHash = "interface:testdev2"
	// Store under normalized ASCII form.
	ifaces := []graph.Iface{
		{
			PHash:        wantPHash,
			Device:       phash.NormDevice("rtr1"),
			IfName:       CanonicalIfName("cisco", "Gi0/1"),
			MetricIfName: "Gi0/1",
			Vendor:       "cisco",
		},
	}
	r := NewResolver(ifaces)

	// Full-width "ｒｔｒ１" (U+FF52 U+FF54 U+FF52 U+FF11)
	fullWidthDev := "ｒｔｒ１"
	got, err := r.Resolve(fullWidthDev, "Gi0/1")
	if err != nil {
		t.Errorf("Resolve(full-width %q): unexpected error: %v", fullWidthDev, err)
	} else if got.PHash != wantPHash {
		t.Errorf("Resolve(full-width %q): got PHash %q; want %q", fullWidthDev, got.PHash, wantPHash)
	}
}

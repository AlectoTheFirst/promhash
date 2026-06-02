package catalog

import "testing"

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
		{"cisco", "Te0/1/2"}:             "tengige0/1/2",
		{"cisco", "Gi0/1"}:               "gigabitethernet0/1",
		{"cisco", "Fa0/1"}:               "fastethernet0/1",
		{"cisco", "Hu1/1"}:               "hundredgige1/1",
		{"arista", "Eth1"}:               "ethernet1",
		{"cisco", "TenGigE0/1/2"}:        "tengige0/1/2",
		{"cisco", "GigabitEthernet0/1"}:  "gigabitethernet0/1",
		{"cisco", "FastEthernet0/1"}:     "fastethernet0/1",
		{"cisco", "HundredGigE1/1"}:      "hundredgige1/1",
		{"arista", "Ethernet1"}:          "ethernet1",
	}
	for in, want := range cases {
		if got := CanonicalIfName(in[0], in[1]); got != want {
			t.Errorf("CanonicalIfName(%q,%q) = %q; want %q", in[0], in[1], got, want)
		}
	}
}

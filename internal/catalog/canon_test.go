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

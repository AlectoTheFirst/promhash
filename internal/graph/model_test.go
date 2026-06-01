package graph

import "testing"

func TestIfaceZeroValueFields(t *testing.T) {
	var i Iface
	if i.IfIndex != 0 || i.PHash != "" {
		t.Fatal("unexpected zero values")
	}
}

package phash

import "testing"

func TestHashCanonicalAndStable(t *testing.T) {
	a := Hash(KindIface, "RTR-Core-1", " Te0/1/2 ")
	b := Hash(KindIface, "rtr-core-1", "te0/1/2")
	if a != b {
		t.Fatalf("canonicalization failed: %q != %q", a, b)
	}
	if got := Hash(KindIface, "rtr-core-1", "te0/1/2"); got != a {
		t.Fatalf("not deterministic: %q != %q", got, a)
	}
	if a[:10] != "interface:" {
		t.Fatalf("missing kind prefix: %q", a)
	}
}

func TestHashDistinctAcrossKindAndKeys(t *testing.T) {
	if Hash(KindDevice, "x") == Hash(KindIface, "x") {
		t.Fatal("kind not in hash")
	}
	if Hash(KindIface, "a") == Hash(KindIface, "b") {
		t.Fatal("keys not in hash")
	}
}

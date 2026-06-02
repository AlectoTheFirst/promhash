package phash

import (
	"strings"
	"testing"
)

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

// TestHashKindDisjoint characterizes the collision properties of Hash per
// entity kind: for identical key parts, every supported Kind must produce a
// distinct id. The kind is both mixed into the hashed bytes and used as the
// prefix, so this holds even for kinds that share a prefix-significant byte
// range. We assert pairwise distinctness across all kinds simultaneously.
//
// Note: hash identity assumes canonical keys are unique per kind. Two entities
// with genuinely distinct upstream identities that happen to normalize to the
// same canonical key (after lower/trim) will share an id; detecting such a
// collision *on merge* is a documented v1 limitation, not a hashing defect.
func TestHashKindDisjoint(t *testing.T) {
	kinds := []Kind{
		KindDevice,
		KindIface,
		KindIP,
		KindEndpoint,
		KindApp,
		KindAppSvc,
		KindBizSvc,
		KindCustomer,
		KindSegment,
	}

	// Identical parts across kinds; the only varying input is the Kind.
	const p1, p2 = "rtr-core-1", "te0/1/2"

	seen := make(map[string]Kind, len(kinds))
	for _, k := range kinds {
		id := Hash(k, p1, p2)

		// Prefix must be the kind itself, so ids are also human-disambiguable.
		wantPrefix := string(k) + ":"
		if !strings.HasPrefix(id, wantPrefix) {
			t.Fatalf("kind %q: id %q missing prefix %q", k, id, wantPrefix)
		}

		if prev, ok := seen[id]; ok {
			t.Fatalf("collision: kind %q and kind %q both hash to %q for identical parts", prev, k, id)
		}
		seen[id] = k
	}

	if len(seen) != len(kinds) {
		t.Fatalf("expected %d distinct ids, got %d", len(kinds), len(seen))
	}
}

// TestHashSeparatorPreventsPartBoundaryCollision documents that the \x1f unit
// separator between joined parts makes part boundaries significant. Without a
// separator, ("a","b") and ("ab") would join to the same byte string and
// collide. Because Hash joins with \x1f, ("a","b") hashes to "a\x1fb" and the
// single part ("ab") hashes to "ab" — genuinely different inputs that must
// produce different ids. The same holds for any re-grouping of the same
// concatenated characters across the part boundary.
func TestNormDevice(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Rtr1", "rtr1"},
		{"  RTR1  ", "rtr1"},
		{"rtr1", "rtr1"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormDevice(tc.in); got != tc.want {
			t.Errorf("NormDevice(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormDeviceMatchesHash(t *testing.T) {
	// The whole point: NormDevice("Rtr1") == the normalization Hash applies,
	// so NormDevice(x) as a map key is always equal to Hash's internal key.
	a := Hash(KindIface, "Rtr1", "te0/1/2")
	b := Hash(KindIface, NormDevice("Rtr1"), "te0/1/2")
	if a != b {
		t.Fatalf("NormDevice diverges from Hash normalization: %q vs %q", a, b)
	}
}

func TestSafeKey(t *testing.T) {
	if err := SafeKey("f", "valid-name"); err != nil {
		t.Errorf("unexpected error for valid value: %v", err)
	}
	if err := SafeKey("f", "a:b"); err == nil {
		t.Error("expected error for colon, got nil")
	}
	if err := SafeKey("f", "a\x01b"); err == nil {
		t.Error("expected error for control char, got nil")
	}
	if err := SafeKey("f", "a\nb"); err == nil {
		t.Error("expected error for newline control char, got nil")
	}
}

func TestHashSeparatorPreventsPartBoundaryCollision(t *testing.T) {
	// Two-part key vs single-part key with the same concatenated characters.
	twoParts := Hash(KindDevice, "a", "b")
	oneJoined := Hash(KindDevice, "ab")
	if twoParts == oneJoined {
		t.Fatalf("part boundary collision: (a,b) and (ab) both hash to %q", twoParts)
	}

	// Re-grouping the boundary must also differ: (ab,c) vs (a,bc).
	left := Hash(KindDevice, "ab", "c")
	right := Hash(KindDevice, "a", "bc")
	if left == right {
		t.Fatalf("part boundary collision: (ab,c) and (a,bc) both hash to %q", left)
	}
}

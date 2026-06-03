// Package phash derives stable, deterministic identifiers for topology
// entities. Each id is a SHA-256 hash of an entity kind plus its key parts,
// case- and whitespace-normalized so that equivalent inputs always produce the
// same id. The kind is both mixed into the hash and used as a human-readable
// prefix on the result.
package phash

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Kind identifies the category of entity an id refers to. It is mixed into the
// hash and used as the prefix of the returned id, so ids of different kinds
// never collide even when their key parts are identical.
type Kind string

// The supported entity kinds. The string value of each is used both as the
// id prefix and as part of the hashed input.
const (
	KindDevice   Kind = "device"
	KindIface    Kind = "interface"
	KindIP       Kind = "ip"
	KindEndpoint Kind = "endpoint"
	KindApp      Kind = "application"
	KindAppSvc   Kind = "appservice"
	KindBizSvc   Kind = "businessservice"
	KindCustomer Kind = "customer"
	KindSegment  Kind = "segment"
)

// NormalizeToken returns the canonical form of a name token: NFKC normalization
// (which folds full-width and other compatibility Unicode variants to their
// ASCII equivalents), followed by Unicode-whitespace collapse (strings.Fields
// covers NBSP U+00A0 and all other Unicode space classes), joined with no
// separator so interior spaces vanish, and finally lowercased.
//
// This is the single shared normalizer applied at every identity site:
// CanonicalIfName (catalog) and NormDevice / Hash (phash). Using NFKC rather
// than NFC is deliberate: NFC only applies canonical decomposition, which does
// not fold full-width characters (U+FF00–U+FF5E) to ASCII; NFKC applies
// compatibility decomposition and does.
func NormalizeToken(s string) string {
	// NFKC folds full-width/compatibility variants to ASCII equivalents.
	s = norm.NFKC.String(s)
	// strings.Fields splits on any Unicode whitespace (including NBSP U+00A0),
	// joining with "" collapses interior spaces and trims outer ones.
	s = strings.Join(strings.Fields(s), "")
	return strings.ToLower(s)
}

// NormDevice returns the canonical form of a device name as applied by Hash:
// NFKC-normalized, interior Unicode whitespace collapsed, and lowercased.
// Store and lookup must both call NormDevice so that the stored key, the hash
// input, and the lookup key are always identical.
func NormDevice(s string) string {
	return NormalizeToken(s)
}

// SafeKey returns an error if value contains ':' or any Unicode control
// character. Both would corrupt the ':'-delimited identity key scheme used
// by Hash and the rest of the catalog pipeline. Apply before storing any
// device or interface name.
func SafeKey(field, value string) error {
	if strings.ContainsRune(value, ':') {
		return fmt.Errorf("%s %q: must not contain ':'", field, value)
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return fmt.Errorf("%s %q: must not contain control characters", field, value)
		}
	}
	return nil
}

// Hash returns a stable id "kind:<16 hex>" over case/space-normalized parts.
// Normalization uses NormalizeToken (NFKC + whitespace collapse + lowercase)
// so Hash, NormDevice, and CanonicalIfName all apply the same transform.
func Hash(k Kind, parts ...string) string {
	normalized := make([]string, len(parts))
	for i, p := range parts {
		normalized[i] = NormalizeToken(p)
	}
	h := sha256.Sum256([]byte(string(k) + "\x1f" + strings.Join(normalized, "\x1f")))
	return string(k) + ":" + hex.EncodeToString(h[:])[:16]
}

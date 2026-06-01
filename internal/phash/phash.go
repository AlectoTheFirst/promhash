// Package phash derives stable, deterministic identifiers for topology
// entities. Each id is a SHA-256 hash of an entity kind plus its key parts,
// case- and whitespace-normalized so that equivalent inputs always produce the
// same id. The kind is both mixed into the hash and used as a human-readable
// prefix on the result.
package phash

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

// Hash returns a stable id "kind:<16 hex>" over case/space-normalized parts.
func Hash(k Kind, parts ...string) string {
	norm := make([]string, len(parts))
	for i, p := range parts {
		norm[i] = strings.ToLower(strings.TrimSpace(p))
	}
	h := sha256.Sum256([]byte(string(k) + "\x1f" + strings.Join(norm, "\x1f")))
	return string(k) + ":" + hex.EncodeToString(h[:])[:16]
}

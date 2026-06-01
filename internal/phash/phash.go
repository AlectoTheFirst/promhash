package phash

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type Kind string

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

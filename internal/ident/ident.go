// Package ident builds deterministic, stable identifiers. IDs are
// content-addressed so equivalent input yields the same identity and the
// same ordering across import, derive, export and re-import.
package ident

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Of returns a prefixed stable id from the canonical JSON encoding of v.
func Of(prefix string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return prefix + "_" + hex.EncodeToString(sum[:])[:20]
}

// Parts builds an id from individually supplied parts (any JSON-able values).
func Parts(prefix string, parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return prefix + "_" + hex.EncodeToString(sum[:])[:16]
}

// Digest returns an unprefixed hex digest over canonical JSON.
func Digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Short is a stable 10-char label for UI display.
func Short(id string) string {
	if len(id) <= 14 {
		return id
	}
	return id[len(id)-10:]
}

var _ = fmt.Sprintf

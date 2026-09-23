// Package ident creates stable, content-derived identifiers. Identical inputs
// always produce the same sequence of ids, so an export/delete/import round
// trip preserves identity, ordering and provenance.
package ident

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func short(prefix string, sum []byte) string {
	return prefix + "_" + hex.EncodeToString(sum[:12])
}

// Of hashes the canonical JSON encoding of v.
func Of(prefix string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return short(prefix, sum[:])
}

// Raw hashes raw bytes.
func Raw(prefix string, b []byte) string {
	sum := sha256.Sum256(b)
	return short(prefix, sum[:])
}

// Derive chains a parent identity with more material.
func Derive(prefix, parent string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	h := sha256.New()
	fmt.Fprintln(h, parent)
	h.Write(b)
	return short(prefix, h.Sum(nil))
}

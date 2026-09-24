// Package identity produces stable, content-based identifiers so that an
// export/delete/import round-trip preserves identity and provenance relations.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Hash returns a 16-char hex digest of the canonical JSON of v.
func Hash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Errorf("identity: cannot hash: %w", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Derive builds "<prefix>-<16 char digest>" from a canonical key-value list.
// Map inputs are sorted so equivalent inputs yield one stable id.
func Derive(prefix string, parts ...any) string {
	return prefix + "-" + Hash(parts)
}

// SortedKeys returns sorted map keys.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// CanonicalKV renders a map as "k=v" pairs sorted by key.
func CanonicalKV[V any](m map[string]V) string {
	parts := make([]string, 0, len(m))
	for _, k := range SortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, ";")
}

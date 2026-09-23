package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// StableID derives a deterministic, content-based identifier with a prefix.
// Equivalent inputs always produce the same id in the same order, which is what
// makes export → delete local DB → import preserve identity/order/provenance.
func StableID(prefix string, canonical any) string {
	b, err := json.Marshal(canonical)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

// ShortID renders a stable short handle; the full id is preserved in payloads.
func ShortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// SortedKeys returns map keys in stable order for canonical summaries.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

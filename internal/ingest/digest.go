package ingest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Canonical renders JSON with sorted map keys so equal content hashes
// identically regardless of source map iteration order.
func Canonical(v any) []byte {
	b, err := json.Marshal(canon(v))
	if err != nil {
		panic(err)
	}
	return b
}

func canon(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		// sort
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
				keys[j-1], keys[j] = keys[j], keys[j-1]
			}
		}
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, canon(t[k])})
		}
		return out
	case []any:
		for i := range t {
			t[i] = canon(t[i])
		}
		return t
	default:
		return v
	}
}

// Digest returns the short stable content digest of v.
func Digest(prefix string, v any) string {
	sum := sha256.Sum256(Canonical(v))
	return prefix + "_" + hex.EncodeToString(sum[:])[:24]
}

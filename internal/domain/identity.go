package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Digest computes a stable content digest over a value using canonical JSON.
// JSON object keys are sorted so semantically equal inputs hash identically.
func Digest(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, err := json.Marshal(canonical(p))
		if err != nil {
			panic("domain: cannot digest " + err.Error())
		}
		h.Write(b)
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonical recursively converts values into a representation whose JSON
// encoding is deterministic (map keys sorted by encoding/json).
func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]struct{ k, v any }, 0, len(t))
		_ = out
		m := make(map[string]any, len(t))
		for _, k := range keys {
			m[k] = canonical(t[k])
		}
		return m
	case []any:
		for i := range t {
			t[i] = canonical(t[i])
		}
		return t
	default:
		return v
	}
}

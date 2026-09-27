package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalJSON serializes a value with sorted object keys and no HTML
// mangling, so equivalent inputs hash identically everywhere.
func CanonicalJSON(v any) []byte {
	b, err := json.Marshal(canonSort(v))
	if err != nil {
		panic(err)
	}
	return b
}

func canonSort(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		// sort.Strings via insertion-free stdlib: implement below
		sortStrings(keys)
		out := make([]struct{ k string; v any }, len(keys))
		for i, k := range keys {
			out[i] = struct{ k string; v any }{k, canonSort(t[k])}
		}
		// json cannot encode anonymous ordered list as object; use jsonOrder.
		return orderedMap(out)
	case []any:
		for i := range t {
			t[i] = canonSort(t[i])
		}
		return t
	default:
		return v
	}
}

type kv struct{ k string; v any }

func orderedMap(pairs []struct{ k string; v any }) json.Marshaler {
	list := make([]kv, len(pairs))
	for i, p := range pairs {
		list[i] = kv(p)
	}
	return jsonOrder(list)
}

// jsonOrder marshals as a JSON object in slice order.
type jsonOrder []kv

func (j jsonOrder) MarshalJSON() ([]byte, error) {
	b := make([]byte, 0, 64)
	b = append(b, '{')
	for i, p := range j {
		if i > 0 {
			b = append(b, ',')
		}
		kb, err := json.Marshal(p.k)
		if err != nil {
			return nil, err
		}
		b = append(b, kb...)
		b = append(b, ':')
		vb, err := json.Marshal(p.v)
		if err != nil {
			return nil, err
		}
		b = append(b, vb...)
	}
	b = append(b, '}')
	return b, nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// Digest returns a hex sha256 of canonical JSON.
func Digest(v any) string {
	sum := sha256.Sum256(CanonicalJSON(v))
	return hex.EncodeToString(sum[:])
}

// ShortID returns id + "-" + 12-char digest.
func ShortID(prefix string, v any) string {
	return prefix + "-" + Digest(v)[:12]
}

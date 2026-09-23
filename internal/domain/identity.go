package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
)

// HashContent returns a short stable sha256 digest over the canonical JSON of
// v. Canonical JSON sorts object keys so equivalent inputs hash identically.
func HashContent(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, err := json.Marshal(canonical(p))
		if err != nil {
			// Fall back to fmt for un-marshalable content.
			b = []byte(strconv.Quote(toString(p)))
		}
		h.Write(b)
		h.Write([]byte{'|'})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ShortID prefixes a 16-char digest with a kind tag.
func ShortID(kind string, parts ...any) string {
	return kind + "_" + HashContent(parts...)
}

func canonical(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([][2]any, 0, len(keys))
		for _, k := range keys {
			out = append(out, [2]any{k, canonical(t[k])})
		}
		return out
	case []any:
		cp := make([]any, len(t))
		for i := range t {
			cp[i] = canonical(t[i])
		}
		return cp
	default:
		return v
	}
}

// CanonicalJSON renders v with sorted object keys.
func CanonicalJSON(v any) string {
	b, _ := json.Marshal(canonical(v))
	return string(b)
}

// Stable edge identity: (capture source digest, channel id, edge index,
// normalized time, normalized level).
func EdgeIdentity(sourceDigest, channelID string, index int, timeNS float64, level int) string {
	return ShortID("E", sourceDigest, channelID, index, formatFloat(timeNS), level)
}

func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

func toString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// EventHash chains events: hash(prev hash, seq, type, payload-canonical).
func EventHash(prev string, seq int64, typ string, payload map[string]any) string {
	return HashContent(prev, strconv.FormatInt(seq, 10), typ, payload)
}

// SortedChanKeys returns channel ids in deterministic order.
func SortedChanKeys(m map[string][]Edge) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// ParamDigest is the digest over all decoder-meaningful parameter fields.
func ParamDigest(p *ParamSet) string {
	return HashContent(
		p.Bus, p.I2CPolarity, p.SCLChannel, p.SDAChannel,
		formatFloat(p.GapThresholdNS),
		p.SPIChannel, p.MOSIChannel, p.MISOChannel,
		p.CSChannels, p.SPIMode, p.BitOrder,
		p.DataChannel, p.ValidChannel, p.WordBits, p.SampleEdge,
		p.BoundarySampling,
	)
}

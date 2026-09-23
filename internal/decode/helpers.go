package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"edgetrace/internal/model"
)

func hash16(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])[:16]
}

func stableID2(prefix string, parts ...string) string {
	return prefix + "_" + hash16(parts...)
}

func candID(x *ctx, kind, hyp string) string {
	return stableID2("cand", kind, x.bus.Name, x.cap.ID, fmtVer(x.par), hyp)
}

// fmtVer renders the parameter version into stable identity material.
func fmtVer(p model.Params) string {
	return strings.Join([]string{
		"v" + itoa(p.Version),
		"b" + p.SampleBoundary,
		"d" + p.DecoderVer,
	}, ",")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

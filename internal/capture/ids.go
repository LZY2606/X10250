package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Stable identities. Edge identity is content addressed so a local source
// change only changes identities of edges that actually changed; sibling and
// ordering relations stay intact for everything else.

func shortHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])[:12]
}

func EdgeID(ch, rawTime, level string, occ int) string {
	return "e_" + shortHash(ch, strings.TrimSpace(rawTime), level, itoa(occ))
}

func ByteID(proto, cs string, indices []int, vals []int) string {
	return "b_" + shortHash(proto, cs, joinInts(indices), joinInts(vals))
}

func TxID(proto, cs string, byteIDs []string) string {
	sorted := append([]string(nil), byteIDs...)
	sort.Strings(sorted)
	return "t_" + shortHash(proto, cs, strings.Join(sorted, ","))
}

func GroupID(byteIDs []string) string {
	return "g_" + shortHash(strings.Join(byteIDs, ","))
}

func RunID(captureID, paramID, digest string) string {
	return "r_" + shortHash("run", captureID, paramID, digest)
}

func ParamID(version int, reason, canonical string) string {
	return "p_" + shortHash("params", itoa(version), reason, canonical)
}

func EvID(kind string, indices []int) string {
	return "v_" + shortHash(kind, joinInts(indices))
}

func CandID(kind, label string, vals []int) string {
	return "c_" + shortHash(kind, label, joinInts(vals))
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

func joinInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = itoa(x)
	}
	return strings.Join(parts, ",")
}

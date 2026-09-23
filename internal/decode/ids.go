package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// stableID derives an identity from deterministic evidence only (no wall
// clock, no insertion order of equivalent inputs).
func stableID(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(h[:])[:12]
}

func byteID(runID, txID string, idx int, bits, variant string) string {
	return "by-" + stableID(runID, txID, fmt.Sprintf("%d", idx), bits, variant)
}

func txID(runID, kind, start, end string) string {
	return "tx-" + stableID(runID, kind, start, end)
}

func candID(runID, target, reason string, values []string) string {
	return "cd-" + stableID(runID, target, reason, strings.Join(values, ","))
}

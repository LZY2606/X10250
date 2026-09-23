package ingest

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash8 returns a stable 12-hex-char content prefix.
func Hash8(parts ...string) string {
	return hash8(joinParts(parts))
}

func hash8(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

func joinParts(parts []string) string {
	out := ""
	for _, p := range parts {
		out += p + "|"
	}
	return out
}

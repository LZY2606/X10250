package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// StableID builds a deterministic, content-addressed identifier.
// Equivalent inputs always produce the same id and ordering key.
func StableID(prefix string, payload interface{}) string {
	canonical, _ := json.Marshal(payload)
	sum := sha256.Sum256(canonical)
	return prefix + "_" + hex.EncodeToString(sum[:])[:20]
}

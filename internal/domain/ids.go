package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Hash10 returns a stable 10-char content hash of the canonical JSON of v.
func Hash10(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:10]
}

// CaptureID is deterministic in the raw material.
func CaptureID(rawText string) string {
	sum := sha256.Sum256([]byte(rawText))
	return "cap_" + hex.EncodeToString(sum[:])[:12]
}

// StableID joins an id-space with a content hash, keeping identity stable
// across export/delete/reimport.
func StableID(prefix string, parts ...any) string {
	b, err := json.Marshal(parts)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return prefix + hex.EncodeToString(sum[:])[:12]
}

// SortedKeys returns deterministic map keys.
func SortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HexByte renders a byte value as 0xNN.
func HexByte(v int) string {
	return fmt.Sprintf("0x%02x", v&0xff)
}

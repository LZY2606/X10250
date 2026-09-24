package ingest

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashID returns a stable 16-char content identity.
func HashID(parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

package model

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashID derives a stable content identity. Identities must survive
// export/delete/reimport, so they are content-addressed rather than
// database-row based.
func HashID(prefix string, parts ...string) string {
	h := sha256.New()
	for i, p := range parts {
		if i > 0 {
			h.Write([]byte{0})
		}
		h.Write([]byte(p))
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:16]
}

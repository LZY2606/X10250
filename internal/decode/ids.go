package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

func hash12(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

func ResultID(captureID, paramsID, inputHash, ver string) string {
	return "res_" + hash12(captureID, paramsID, inputHash, ver)
}

func SlotID(captureID, protocol, device string, frameKey string, order int) string {
	return fmt.Sprintf("slot_%s", hash12(captureID, protocol, device, frameKey, order))
}

func CandidateID(resultID, altGroup string, key []any) string {
	return fmt.Sprintf("cand_%s", hash12(append([]any{resultID, altGroup}, key...)...))
}

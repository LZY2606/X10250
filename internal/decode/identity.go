package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

func hashObject(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func uid(prefix, key string) string {
	sum := sha256.Sum256([]byte(key))
	return prefix + "_" + hex.EncodeToString(sum[:8])
}

// ByteUID / trxUID are deterministic on bus, group identity and indices.
func ByteUID(bus, group string, line string, idx int) string {
	return uid("byte", bus+"|"+group+"|"+line+"|"+strconv.Itoa(idx))
}

func TrxUID(bus, key string) string {
	return uid("trx", bus+"|"+key)
}

func GroupUID(bus, key string) string {
	return uid("grp", bus+"|"+key)
}

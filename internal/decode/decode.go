// Package decode turns immutable edge captures into reviewable bus traffic.
// All rules live here (server/storage callable, no browser logic):
// I2C/SPI/custom-sync framing, evidence, candidates and diagnostics.
package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

const MaxBytesPerRun = 65536

// Options controls a decode run. Clock polarity etc. come from the profile;
// ambiguity is resolved by keeping candidates rather than assuming defaults.
type Options struct {
	Bus           string
	BusName       string
	Params        map[string]string
	MaxBytes      int
	StretchCycles int64 // expected SCL period in ps; longer low-time => stretching
}

// Decode is the entry point used by the app layer and tests directly.
func Decode(cap *domain.Capture, opt Options) (*domain.RunResult, *domain.Error) {
	if cap == nil {
		return nil, domain.ErrSemantic(0, 0, "missing capture")
	}
	if opt.MaxBytes == 0 {
		opt.MaxBytes = MaxBytesPerRun
	}
	switch opt.Bus {
	case domain.BusI2C:
		return decodeI2C(cap, opt)
	case domain.BusSPI:
		return decodeSPI(cap, opt)
	case domain.BusSync:
		return decodeSync(cap, opt)
	default:
		return nil, domain.ErrSemantic(0, 0, "unknown bus kind: "+opt.Bus)
	}
}

// InputDigest summarizes the exact material+parameters a run consumed.
func InputDigest(cap *domain.Capture, opt Options) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%s|%s|", cap.ID, cap.RawSHA256, opt.Bus)
	keys := make([]string, 0, len(opt.Params))
	for k := range opt.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s;", k, opt.Params[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func MarshalResult(r *domain.RunResult) json.RawMessage {
	b, _ := json.Marshal(r)
	return b
}

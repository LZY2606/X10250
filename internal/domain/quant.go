package domain

import (
	"math/big"
	"strings"
)

// Q is a physical quantity. The original textual representation (Raw/Unit) is
// immutable material. Value is the derived, normalized magnitude expressed in
// BaseUnit. When the representation cannot be converted, Ok is false and the
// value stays in place together with a diagnostic instead of being dropped.
type Q struct {
	Raw      string   `json:"raw"`
	Unit     string   `json:"unit"`
	BaseUnit string   `json:"baseUnit"`
	Value    *big.Rat `json:"-"`
	Ok       bool     `json:"convertible"`
}

func (q Q) String() string {
	if q.Raw != "" {
		return q.Raw
	}
	if q.Value != nil {
		return q.Value.RatString() + q.BaseUnit
	}
	return ""
}

// MarshalJSON emits the normalized value as an exact rational string so no
// precision is hidden by float64 on the review surface.
func (q Q) MarshalJSON() ([]byte, error) {
	type shadow struct {
		Raw      string `json:"raw"`
		Unit     string `json:"unit"`
		BaseUnit string `json:"baseUnit"`
		Value    string `json:"value"`
		Ok       bool   `json:"convertible"`
	}
	s := shadow{Raw: q.Raw, Unit: q.Unit, BaseUnit: q.BaseUnit, Ok: q.Ok}
	if q.Value != nil {
		s.Value = q.Value.RatString()
	}
	return jsonMarshal(s)
}

var timeFactors = map[string]string{
	"ps": "1",
	"ns": "1000",
	"us": "1000000",
	"µs": "1000000",
	"μs": "1000000",
	"ms": "1000000000",
	"s":  "1000000000000",
}

var freqFactors = map[string]string{
	"hz":  "1",
	"khz": "1000",
	"mhz": "1000000",
	"ghz": "1000000000",
}

// ParseTime converts "12.5ns", "3us", "7" (picoseconds by domain default) into
// a picosecond rational. The raw representation is preserved on the returned Q.
func ParseTime(raw string) Q {
	q := Q{Raw: raw, Unit: "ps", BaseUnit: "ps"}
	n, u := splitNumberUnit(raw)
	if u == "" {
		u = "ps"
	}
	f, ok := timeFactors[strings.ToLower(u)]
	if !ok {
		f, ok = timeFactors[u]
	}
	if !ok {
		q.Unit = u
		return q
	}
	r, ok := parseRat(n)
	if !ok || r.Sign() < 0 {
		return q
	}
	factor, _ := new(big.Rat).SetString(f)
	q.Unit = u
	q.Value = new(big.Rat).Mul(r, factor)
	q.Ok = true
	return q
}

// ParseFreq converts "100MHz", "400kHz" into hertz rationals.
func ParseFreq(raw string) Q {
	q := Q{Raw: raw, Unit: "Hz", BaseUnit: "Hz"}
	n, u := splitNumberUnit(raw)
	f, ok := freqFactors[strings.ToLower(u)]
	if !ok {
		q.Unit = u
		return q
	}
	r, ok := parseRat(n)
	if !ok || r.Sign() < 0 {
		return q
	}
	factor, _ := new(big.Rat).SetString(f)
	q.Unit = u
	q.Value = new(big.Rat).Mul(r, factor)
	q.Ok = true
	return q
}

// ParseVolts converts "1.8V", "3300mV" into volts; raw always retained.
func ParseVolts(raw string) Q {
	q := Q{Raw: raw, Unit: "V", BaseUnit: "V"}
	n, u := splitNumberUnit(raw)
	factors := map[string]string{"v": "1", "mv": "1/1000"}
	f, ok := factors[strings.ToLower(u)]
	if !ok {
		q.Unit = u
		return q
	}
	r, ok := parseRat(n)
	if !ok || r.Sign() < 0 {
		return q
	}
	factor, _ := new(big.Rat).SetString(f)
	q.Unit = u
	q.Value = new(big.Rat).Mul(r, factor)
	q.Ok = true
	return q
}

func splitNumberUnit(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '/' || c == '-' || c == '+' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
}

func parseRat(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	if r, ok := new(big.Rat).SetString(s); ok {
		return r, true
	}
	return nil, false
}

// Package qty parses physical quantities used by the edge bench.
//
// The raw textual representation of every quantity is preserved by callers.
// Normalization converts exact decimal time expressions to an integer number
// of picoseconds. A value that cannot be converted exactly is reported with a
// reason instead of being silently rounded or replaced by a default.
package qty

import (
	"math/big"
	"strings"
)

// TimeResult is the outcome of parsing one time expression.
type TimeResult struct {
	Raw        string
	Picoseconds int64
	OK         bool
	Reason     string
}

var timeUnits = map[string]int64{
	"ps":  1,
	"ns":  1_000,
	"us":  1_000_000,
	"µs":  1_000_000,
	"ms":  1_000_000_000,
	"s":   1_000_000_000_000,
	"sec": 1_000_000_000_000,
}

// ParseTime parses values such as "12.5 ns" or "3" (picoseconds when bare).
func ParseTime(raw string) TimeResult {
	res := TimeResult{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" {
		res.Reason = "empty time expression"
		return res
	}
	numPart, unit := splitNumberUnit(s)
	scale := int64(1)
	if unit != "" {
		factor, ok := timeUnits[strings.ToLower(unit)]
		if !ok {
			res.Reason = "unknown time unit: " + unit
			return res
		}
		scale = factor
	}
	r, ok := new(big.Rat).SetString(numPart)
	if !ok {
		res.Reason = "not a decimal number: " + numPart
		return res
	}
	if r.Sign() < 0 {
		res.Reason = "negative time"
		return res
	}
	ps := new(big.Rat).Mul(r, big.NewRat(scale, 1))
	if !ps.IsInt() {
		res.Reason = "time does not convert to whole picoseconds: " + raw
		return res
	}
	if ps.Num().Cmp(big.NewInt(maxInt64)) > 0 {
		res.Reason = "time overflows int64 picoseconds: " + raw
		return res
	}
	res.Picoseconds = ps.Num().Int64()
	res.OK = true
	return res
}

const maxInt64 = int64(9223372036854775807)

// RateResult keeps a rate in hertz as an exact rational plus the raw text.
type RateResult struct {
	Raw    string
	Hertz  *big.Rat
	PeriodPs *big.Rat // period length in picoseconds (may be fractional)
	OK     bool
	Reason string
}

var rateUnits = map[string]int64{
	"hz":  1,
	"khz": 1_000,
	"mhz": 1_000_000,
	"ghz": 1_000_000_000,
}

// ParseRate parses values such as "10 MHz" or "400kHz".
func ParseRate(raw string) RateResult {
	res := RateResult{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" {
		res.Reason = "empty rate expression"
		return res
	}
	numPart, unit := splitNumberUnit(s)
	scale := int64(1)
	if unit != "" {
		factor, ok := rateUnits[strings.ToLower(unit)]
		if !ok {
			res.Reason = "unknown rate unit: " + unit
			return res
		}
		scale = factor
	}
	r, ok := new(big.Rat).SetString(numPart)
	if !ok {
		res.Reason = "not a decimal number: " + numPart
		return res
	}
	if r.Sign() <= 0 {
		res.Reason = "rate must be strictly positive: " + raw
		return res
	}
	hz := new(big.Rat).Mul(r, big.NewRat(scale, 1))
	res.Hertz = hz
	res.PeriodPs = new(big.Rat).Quo(big.NewRat(1_000_000_000_000, 1), hz)
	res.OK = true
	return res
}

func splitNumberUnit(s string) (number, unit string) {
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || c == '/' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	number = strings.TrimSpace(s[:i])
	unit = strings.TrimSpace(s[i:])
	return
}

// FormatPs renders an integer picosecond count keeping the raw text attached at
// call sites; used for coordinate tooltips in the review UI.
func FormatPs(ps int64) string {
	switch {
	case ps >= 1_000_000_000_000 && ps%1_000_000_000_000 == 0:
		return big.NewRat(ps, 1_000_000_000_000).FloatString(6) + " s"
	case ps >= 1_000_000 && ps%1_000_000 == 0:
		return big.NewRat(ps, 1_000_000).FloatString(3) + " ms"
	case ps >= 1_000 && ps%1_000 == 0:
		return big.NewRat(ps, 1_000).FloatString(3) + " us"
	default:
		return big.NewInt(ps).String() + " ps"
	}
}

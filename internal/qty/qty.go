// Package qty models measured quantities that carry their original textual
// representation and coordinate basis. The raw value is immutable evidence;
// normalization is a derived layer. Values that cannot be converted remain at
// their original position with a diagnostic instead of being replaced by a
// default or dropped for rendering.
package qty

import (
	"strconv"
	"strings"
)

// Basis identifies what a quantity is measured against.
type Basis string

const (
	BasisTime Basis = "time" // normalized unit: picoseconds from capture t0
	BasisRate Basis = "rate" // normalized unit: hertz
)

// Quantity is an optionally-normalized measured value.
type Quantity struct {
	// Raw is the exact representation as imported; it is never rewritten.
	Raw string `json:"raw"`
	// Basis is "time" or "rate".
	Basis Basis `json:"basis"`
	// Unit is the declared unit (e.g. "ns", "MHz").
	Unit string `json:"unit"`
	// Value is the normalized SI value (picoseconds or hertz).
	Value int64 `json:"value,omitempty"`
	// OK reports whether Raw could be converted exactly enough to be used.
	OK bool `json:"ok"`
	// Reason explains why the value could not be converted.
	Reason string `json:"reason,omitempty"`
}

var timeScale = map[string]int64{
	"ps": 1,
	"ns": 1_000,
	"us": 1_000_000, "µs": 1_000_000,
	"ms": 1_000_000_000,
	"s":  1_000_000_000_000,
}

var rateScale = map[string]int64{
	"hz":  1,
	"khz": 1_000,
	"mhz": 1_000_000,
	"ghz": 1_000_000_000,
}

func splitNumber(s string) (num, unit string) {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) {
		c := s[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '-' || c == '+' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i:])
}

// ParseTime parses an instant or offset. The unit suffix is mandatory so a
// unitless number is never silently interpreted.
func ParseTime(raw string) Quantity {
	num, unit := splitNumber(raw)
	q := Quantity{Raw: raw, Basis: BasisTime, Unit: unit}
	scale, ok := timeScale[strings.ToLower(unit)]
	if !ok {
		q.Reason = "unknown or missing time unit (expected ps/ns/us/ms/s)"
		return q
	}
	v, err := parseScaled(num, scale)
	if err != nil {
		q.Reason = err.Error()
		return q
	}
	q.Value, q.OK = v, true
	return q
}

// ParseRate parses a frequency/sample rate with an explicit unit suffix.
func ParseRate(raw string) Quantity {
	num, unit := splitNumber(raw)
	q := Quantity{Raw: raw, Basis: BasisRate, Unit: unit}
	scale, ok := rateScale[strings.ToLower(unit)]
	if !ok {
		q.Reason = "unknown or missing rate unit (expected Hz/kHz/MHz/GHz)"
		return q
	}
	v, err := parseScaled(num, scale)
	if err != nil {
		q.Reason = err.Error()
		return q
	}
	q.Value, q.OK = v, true
	return q
}

func parseScaled(num string, scale int64) (int64, error) {
	if num == "" {
		return 0, strconv.ErrSyntax
	}
	if strings.ContainsAny(num, ".eE") {
		f, err := strconv.ParseFloat(num, 64)
		if err != nil {
			return 0, err
		}
		return int64(f*float64(scale) + 0.5), nil
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return 0, err
	}
	return n * scale, nil
}

// FormatPS renders picoseconds with an engineering unit for display only.
func FormatPS(ps int64) string {
	switch {
	case ps%1_000_000_000_000 == 0:
		return strconv.FormatInt(ps/1_000_000_000_000, 10) + "s"
	case ps%1_000_000_000 == 0:
		return strconv.FormatInt(ps/1_000_000_000, 10) + "ms"
	case ps%1_000_000 == 0:
		return strconv.FormatInt(ps/1_000_000, 10) + "us"
	case ps%1_000 == 0:
		return strconv.FormatInt(ps/1_000, 10) + "ns"
	default:
		return strconv.FormatInt(ps, 10) + "ps"
	}
}

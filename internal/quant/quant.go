// Package quant parses and retains physical quantities with their original
// textual representation, units and source position. Normalisation to SI base
// units happens only in the derived layer; unconvertible values stay in place
// with a diagnostic instead of being silently replaced by a default.
package quant

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Pos is a 1-based source location inside an imported artifact.
type Pos struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

func (p Pos) String() string {
	if p.File == "" {
		return fmt.Sprintf("%d:%d", p.Line, p.Col)
	}
	return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Col)
}

// Q is a quantity. Raw is exactly what the user supplied; Unit is the declared
// unit; Value is the SI-base normalised value (0 when OK is false).
type Q struct {
	Raw   string  `json:"raw"`
	Unit  string  `json:"unit"`
	Value float64 `json:"value"`
	OK    bool    `json:"ok"`
	Pos   Pos     `json:"pos"`
}

// Diagnostic explains why a piece of raw input could not be normalised.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Pos     Pos    `json:"pos"`
	Field   string `json:"field,omitempty"`
}

func (d Diagnostic) Error() string {
	if d.Field != "" {
		return fmt.Sprintf("%s %s: %s", d.Pos, d.Field, d.Message)
	}
	return fmt.Sprintf("%s %s", d.Pos, d.Message)
}

var unitScale = map[string]float64{
	// time
	"s": 1, "ms": 1e-3, "us": 1e-6, "µs": 1e-6, "ns": 1e-9,
	// frequency
	"Hz": 1, "kHz": 1e3, "MHz": 1e6, "GHz": 1e9,
	// voltage (probe thresholds are kept in volts)
	"V": 1, "mV": 1e-3,
	// dimensionless
	"": 1,
}

// Parse parses tokens such as "100us", "12.5MHz", "0.8V", "42". A bare number
// is accepted only when expectUnit is empty (dimensionless).
func Parse(raw string, pos Pos, expectUnit string) (Q, *Diagnostic) {
	raw = strings.TrimSpace(raw)
	q := Q{Raw: raw, Pos: pos}
	i := 0
	for i < len(raw) {
		c := raw[i]
		if (c >= '0' && c <= '9') || c == '.' || c == '+' || c == '-' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return q, &Diagnostic{Code: "QUANT_NO_NUMBER", Message: fmt.Sprintf("no numeric component in %q", raw), Pos: pos}
	}
	num, unit := raw[:i], strings.TrimSpace(raw[i:])
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) {
		return q, &Diagnostic{Code: "QUANT_BAD_NUMBER", Message: fmt.Sprintf("bad number %q", num), Pos: pos}
	}
	scale, known := unitScale[unit]
	if !known {
		return q, &Diagnostic{Code: "QUANT_UNKNOWN_UNIT", Message: fmt.Sprintf("unknown unit %q", unit), Pos: pos}
	}
	if expectUnit != "" && unit != "" && !sameDimension(unit, expectUnit) {
		return q, &Diagnostic{Code: "QUANT_WRONG_DIMENSION", Message: fmt.Sprintf("unit %q is not a %s", unit, expectUnit), Pos: pos}
	}
	q.Unit = unit
	q.Value = v * scale
	q.OK = true
	return q, nil
}

func sameDimension(unit, expect string) bool {
	time := map[string]bool{"s": true, "ms": true, "us": true, "µs": true, "ns": true}
	freq := map[string]bool{"Hz": true, "kHz": true, "MHz": true, "GHz": true}
	volt := map[string]bool{"V": true, "mV": true}
	switch expect {
	case "s":
		return time[unit]
	case "Hz":
		return freq[unit]
	case "V":
		return volt[unit]
	}
	return unit == expect
}

// Format renders an SI-base value back into a compact engineering string.
func Format(v float64, base string) string {
	prefixes := []struct {
		s string
		f float64
	}{
		{"G", 1e9}, {"M", 1e6}, {"k", 1e3}, {"", 1}, {"m", 1e-3}, {"\u00b5", 1e-6}, {"n", 1e-9},
	}
	if v == 0 {
		return "0" + base
	}
	a := math.Abs(v)
	for _, p := range prefixes {
		if a >= p.f && (p.f == 1 || a < p.f*1e3) {
			return trimZ(strconv.FormatFloat(v/p.f, 'f', 4, 64)) + p.s + base
		}
	}
	return trimZ(strconv.FormatFloat(v, 'g', 6, 64)) + base
}

func trimZ(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}

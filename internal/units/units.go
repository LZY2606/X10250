// Package units handles physical quantities: the original representation is
// always retained, normalization to a canonical unit happens only in the
// derived layer, and values that cannot be converted stay in place with a
// positioned diagnostic instead of being replaced by a zero/default.
package units

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Kind int

const (
	Dimensionless Kind = iota
	Time
	Frequency
	Voltage
)

// Measure is a measurement that remembers how it was written in the source
// document plus the normalized value used by decoders.
type Measure struct {
	Kind       Kind    `json:"kind"`
	Raw        string  `json:"raw"`
	Unit       string  `json:"unit"`
	Value      float64 `json:"value"`
	Normalized float64 `json:"normalized"`
	NormalUnit string  `json:"normalUnit"`
	OK         bool    `json:"ok"`
	Diag       string  `json:"diag,omitempty"`
}

var factors = map[string]struct {
	kind   Kind
	factor float64 // multiply raw value to get canonical unit
	canon  string
}{
	"s":   {Time, 1, "s"},
	"ms":  {Time, 1e-3, "s"},
	"us":  {Time, 1e-6, "s"},
	"µs":  {Time, 1e-6, "s"},
	"ns":  {Time, 1e-9, "s"},
	"ps":  {Time, 1e-12, "s"},
	"hz":  {Frequency, 1, "Hz"},
	"khz": {Frequency, 1e3, "Hz"},
	"mhz": {Frequency, 1e6, "Hz"},
	"v":   {Voltage, 1, "V"},
	"mv":  {Voltage, 1e-3, "V"},
}

var kindName = map[Kind]string{
	Dimensionless: "dimensionless",
	Time:          "time",
	Frequency:     "frequency",
	Voltage:       "voltage",
}

func (k Kind) String() string { return kindName[k] }

// Parse converts "12.5us" into a Measure. A missing unit is dimensionless.
func Parse(raw string) Measure {
	s := strings.TrimSpace(raw)
	num, unit := split(s)
	m := Measure{Raw: raw, Unit: unit}
	if s == "" {
		m.Diag = "empty measure"
		return m
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		m.Diag = fmt.Sprintf("unparsable number %q", num)
		return m
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		m.Diag = "non-finite measure"
		return m
	}
	m.Value = v
	if unit == "" {
		m.Kind = Dimensionless
		m.Normalized = v
		m.OK = true
		return m
	}
	f, ok := factors[strings.ToLower(unit)]
	if !ok {
		m.Diag = fmt.Sprintf("unknown unit %q; value kept at original position", unit)
		return m
	}
	m.Kind = f.kind
	m.Normalized = v * f.factor
	m.NormalUnit = f.canon
	m.OK = true
	return m
}

// ParseOf parses and requires the expected dimension.
func ParseOf(raw string, want Kind) Measure {
	m := Parse(raw)
	if !m.OK {
		return m
	}
	if m.Kind != want {
		m.OK = false
		m.Diag = fmt.Sprintf("expected %s, got %s (%q)", want, m.Kind, raw)
	}
	return m
}

// Require converts and requires the expected dimension, returning an error
// carrying the original token so callers never substitute a default.
func Require(raw string, want Kind) (float64, Measure, error) {
	m := ParseOf(raw, want)
	if !m.OK {
		return 0, m, errors.New(m.Diag)
	}
	return m.Normalized, m, nil
}

func split(s string) (string, string) {
	i := len(s)
	for i > 0 {
		r := rune(s[i-1])
		if (r >= '0' && r <= '9') || r == '.' || r == '+' || r == '-' || r == 'e' || r == 'E' {
			break
		}
		i--
	}
	if i == 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}

// Format renders a normalized value with its canonical unit.
func Format(kind Kind, v float64) string {
	for _, name := range []string{"s", "ms", "us", "ns"} {
		f := factors[name]
		if f.kind == kind && v >= f.factor && v < f.factor*1000 {
			return fmt.Sprintf("%g%s", v/f.factor, name)
		}
	}
	canon := map[Kind]string{Time: "s", Frequency: "Hz", Voltage: "V", Dimensionless: ""}[kind]
	if f, ok := factors[strings.ToLower(canon)]; ok {
		return fmt.Sprintf("%g%s", v/f.factor, canon)
	}
	return fmt.Sprintf("%g", v)
}

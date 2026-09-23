// Package measure parses and normalises physical quantities.
//
// Raw textual representations are preserved end-to-end; the normalised value
// lives on a derived layer. Values that cannot be converted stay where they are
// together with a diagnostic instead of being silently defaulted.
package measure

import (
	"math/big"
	"strings"
)

type Dimension string

const (
	Time      Dimension = "time"
	Frequency Dimension = "frequency"
	Voltage   Dimension = "voltage"
)

// Quantity is the immutable raw value plus the derived normalisation.
type Quantity struct {
	Raw    string    `json:"raw"`
	Dim    Dimension `json:"dim"`
	Norm   int64     `json:"norm,omitempty"` // ps, µHz, µV
	Unit   string    `json:"unit,omitempty"`
	OK     bool      `json:"ok"`
	Reason string    `json:"reason,omitempty"`
}

type unit struct {
	scale *big.Rat
	dim   Dimension
}

var units = func() map[string]unit {
	m := map[string]unit{}
	add := func(dim Dimension, scale *big.Rat, names ...string) {
		for _, n := range names {
			m[n] = unit{scale: scale, dim}
		}
	}
	r := func(f float64) *big.Rat { return new(big.Rat).SetFloat64(f) }
	// time: normalised unit is the picosecond
	add(Time, big.NewRat(1, 1e12), "s")
	add(Time, big.NewRat(1, 1e9), "ms")
	add(Time, big.NewRat(1, 1e6), "us", "µs")
	add(Time, big.NewRat(1, 1e3), "ns")
	add(Time, big.NewRat(1, 1), "ps")
	// frequency: normalised unit is the microhertz
	add(Frequency, big.NewRat(1e6, 1), "hz")
	add(Frequency, big.NewRat(1e9, 1), "khz")
	add(Frequency, big.NewRat(1e12, 1), "mhz")
	add(Frequency, big.NewRat(1e15, 1), "ghz")
	// voltage: normalised unit is the microvolt
	add(Voltage, big.NewRat(1e6, 1), "v")
	add(Voltage, big.NewRat(1, 1), "uv", "µv")
	add(Voltage, big.NewRat(1000, 1), "mv")
	_ = r
	return m
}()

// Parse converts "12.5ns", "400kHz", "3.3V", ... into a Quantity.
func Parse(dim Dimension, raw string) Quantity {
	q := Quantity{Raw: raw, Dim: dim}
	s := strings.TrimSpace(raw)
	if s == "" {
		q.Reason = "empty quantity"
		return q
	}
	i := 0
	for i < len(s) && (s[i] == '+' || s[i] == '-' || (s[i] >= '0' && s[i] <= '9') || s[i] == '.' || s[i] == 'e' || s[i] == 'E') {
		i++
	}
	if i == 0 {
		q.Reason = "no numeric part"
		return q
	}
	num, ok := new(big.Rat).SetString(s[:i])
	if !ok {
		q.Reason = "bad numeric part"
		return q
	}
	name := strings.ToLower(strings.TrimSpace(s[i:]))
	if name == "" {
		q.Reason = "missing unit"
		return q
	}
	u, found := units[name]
	if !found {
		q.Reason = "unknown unit " + name
		return q
	}
	if u.dim != dim {
		q.Reason = "unit " + name + " is not a " + string(dim)
		return q
	}
	norm := new(big.Rat).Mul(num, u.scale)
	if !norm.IsInt() {
		q.Reason = "value does not map to an integral " + normUnit(dim)
		return q
	}
	n := norm.Num()
	if !n.IsInt64() {
		q.Reason = "magnitude overflow"
		return q
	}
	q.Norm = n.Int64()
	q.Unit = name
	q.OK = true
	return q
}

func normUnit(d Dimension) string {
	switch d {
	case Time:
		return "picosecond"
	case Frequency:
		return "microhertz"
	default:
		return "microvolt"
	}
}

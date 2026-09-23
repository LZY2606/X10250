// Package domain defines the immutable evidence model and the mutable decision
// model of the edge decoding bench. Quantities always carry their original
// representation plus a unit/coordinate basis; derived layers may normalize
// them, but the raw representation is never discarded.
package domain

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
)

// Quantity is a physical quantity with its original textual representation and
// a unit basis. Norm is the value normalized to BaseUnit (nil if conversion was
// impossible); in that case Convertible is false and a diagnostic keeps the
// value at its original position instead of faking a normalized result.
type Quantity struct {
	Raw         string  `json:"raw"`
	Unit        string  `json:"unit"`
	BaseUnit    string  `json:"base_unit"`
	Norm        *big.Float `json:"-"`
	Convertible bool    `json:"convertible"`
}

// Integer-picosecond physical basis used by the derived layer.
var timeFactorsPS = map[string]int64{
	"ps": 1, "ns": 1_000, "us": 1_000_000, "\u00b5s": 1_000_000, "ms": 1_000_000_000, "s": 1_000_000_000_000,
}

var rateFactorsHz = map[string]int64{
	"hz": 1, "khz": 1_000, "mhz": 1_000_000, "ghz": 1_000_000_000,
}

// ParseQuantity keeps the original representation and computes the normalized
// value in the SI base of its dimension. Unconvertible values stay at the
// original position with Convertible=false.
func ParseQuantity(raw, unit string, kind string) Quantity {
	q := Quantity{Raw: strings.TrimSpace(raw), Unit: strings.TrimSpace(unit)}
	switch kind {
	case "time":
		q.BaseUnit = "ps"
		if f, ok := parseUnitNumber(q.Raw); ok {
			if fac, has := timeFactorsPS[strings.ToLower(q.Unit)]; has {
				q.Norm = new(big.Float).Mul(f, big.NewFloat(float64(fac)))
				q.Convertible = true
			}
		}
	case "rate":
		q.BaseUnit = "Hz"
		if f, ok := parseUnitNumber(q.Raw); ok {
			if fac, has := rateFactorsHz[strings.ToLower(q.Unit)]; has {
				q.Norm = new(big.Float).Mul(f, big.NewFloat(float64(fac)))
				q.Convertible = true
			}
		}
	default:
		q.BaseUnit = ""
		if f, ok := parseUnitNumber(q.Raw); ok {
			q.Norm = f
			q.Convertible = true
		}
	}
	return q
}

func parseUnitNumber(s string) (*big.Float, bool) {
	if s == "" {
		return nil, false
	}
	f, _, err := big.ParseFloat(s, 10, 64, big.ToNearestEven)
	if err != nil {
		return nil, false
	}
	return f, true
}

// Int64PS returns the normalized time in picoseconds.
func (q Quantity) Int64PS() (int64, bool) {
	if !q.Convertible || q.Norm == nil || q.BaseUnit != "ps" {
		return 0, false
	}
	v, _ := q.Norm.Int64()
	return v, true
}

// Int64Hz returns the normalized rate in hertz.
func (q Quantity) Int64Hz() (int64, bool) {
	if !q.Convertible || q.Norm == nil || q.BaseUnit != "Hz" {
		return 0, false
	}
	v, _ := q.Norm.Int64()
	return v, true
}

// MarshalJSON keeps norm value present as a decimal string when available.
func (q Quantity) MarshalJSON() ([]byte, error) {
	type alias struct {
		Raw         string `json:"raw"`
		Unit        string `json:"unit"`
		BaseUnit    string `json:"base_unit"`
		Norm        string `json:"norm"`
		Convertible bool   `json:"convertible"`
	}
	a := alias{Raw: q.Raw, Unit: q.Unit, BaseUnit: q.BaseUnit, Convertible: q.Convertible}
	if q.Norm != nil {
		a.Norm = q.Norm.Text('f', -1)
	}
	return json.Marshal(a)
}

// Position points at a piece of source material by JSON pointer plus
// 1-based line/column/offset into the original import document.
type Position struct {
	JSONPointer string `json:"json_pointer"`
	Line        int    `json:"line,omitempty"`
	Column      int    `json:"column,omitempty"`
	Offset      int    `json:"offset,omitempty"`
}

func (p Position) String() string {
	if p.JSONPointer != "" {
		if p.Line > 0 {
			return fmt.Sprintf("%s@%d:%d", p.JSONPointer, p.Line, p.Column)
		}
		return p.JSONPointer
	}
	if p.Offset >= 0 && p.Line > 0 {
		return fmt.Sprintf("offset %d (%d:%d)", p.Offset, p.Line, p.Column)
	}
	return "?"
}

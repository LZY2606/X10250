// Package units models physical quantities with their original textual
// representation intact. Normalization happens only on the derived layer;
// values that cannot be converted are kept in place with a diagnostic
// instead of being silently dropped or replaced by a default.
package units

import (
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Kind classifies a measured quantity.
type Kind string

const (
	KindTime      Kind = "time"
	KindFrequency Kind = "rate"
	KindVoltage   Kind = "voltage"
	KindDimension Kind = "dimension"
	KindUnitless  Kind = "unitless"
)

// Quantity is an original measurement. Raw is the exact text supplied at
// import; it is never overwritten. Unit is the original unit token. When
// the value can be normalized, Normalized holds the SI value and Normal is
// the target unit; otherwise Diagnostic explains why and the value stays
// in Raw.
type Quantity struct {
	Kind       Kind    `json:"kind"`
	Raw        string  `json:"raw"`
	Unit       string  `json:"unit"`
	Normal     string  `json:"normalUnit,omitempty"`
	Normalized float64 `json:"normalized,omitempty"`
	Valid      bool    `json:"valid"`
	Diagnostic string  `json:"diagnostic,omitempty"`
}

// Diagnostic points at a field/position and explains a conversion failure.
type Diagnostic struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

func (d Diagnostic) Error() string { return d.Path + ": " + d.Message }

// Factors maps a normalized unit token to (factor, kind).
var factors = map[string]struct {
	Factor float64
	Kind   Kind
}{
	"ps": {1e-12, KindTime}, "ns": {1e-9, KindTime}, "us": {1e-6, KindTime},
	"µs": {1e-6, KindTime}, "ms": {1e-3, KindTime}, "s": {1, KindTime},
	"hz": {1, KindFrequency}, "khz": {1e3, KindFrequency}, "mhz": {1e6, KindFrequency},
	"ghz": {1e9, KindFrequency}, "sa/s": {1, KindFrequency}, "sps": {1, KindFrequency},
	"v": {1, KindVoltage}, "mv": {1e-3, KindVoltage},
	"": {1, KindUnitless},
}

// Parse converts an original value/unit token into a Quantity. The raw
// strings are preserved verbatim. expectHint optionally constrains the
// dimension; "" accepts any.
func Parse(raw, unit string, expect Kind) Quantity {
	q := Quantity{Raw: strings.TrimSpace(raw), Unit: strings.TrimSpace(unit), Kind: expect}
	text := strings.TrimSpace(raw)
	if text == "" {
		q.Diagnostic = "empty value; no default is assumed"
		return q
	}
	// Reject infinity/NaN explicitly rather than normalizing them.
	if strings.EqualFold(text, "nan") || strings.Contains(strings.ToLower(text), "inf") {
		q.Diagnostic = "non-finite value"
		return q
	}
	f, _, err := big.ParseFloat(text, 10, 64, big.ToNearestEven)
	if err != nil {
		q.Diagnostic = "cannot parse number: " + err.Error()
		return q
	}
	v, _ := f.Float64()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		q.Diagnostic = "non-finite value"
		return q
	}
	info, ok := factors[strings.ToLower(q.Unit)]
	if !ok {
		q.Diagnostic = "unknown unit " + strconvQuote(q.Unit)
		return q
	}
	if expect != "" && expect != KindUnitless && info.Kind != expect && info.Kind != KindUnitless {
		q.Diagnostic = fmt.Sprintf("unit %q is not a %s", q.Unit, expect)
		return q
	}
	if v < 0 && expect == KindFrequency {
		q.Diagnostic = "frequency must be non-negative"
		return q
	}
	q.Valid = true
	q.Normalized = v * info.Factor
	switch expect {
	case KindTime:
		q.Normal = "s"
	case KindFrequency:
		q.Normal = "Hz"
	case KindVoltage:
		q.Normal = "V"
	default:
		q.Normal = ""
	}
	return q
}

func strconvQuote(s string) string {
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	for _, r := range s {
		switch r {
		case '"':
			b = append(b, '\\', '"')
		case '\\':
			b = append(b, '\\', '\\')
		default:
			b = append(b, string(r)...)
		}
	}
	b = append(b, '"')
	return string(b)
}

// Picoseconds renders a SI-normalized time quantity as an integer
// picosecond value used by the decoders. Returns ok=false with a
// diagnostic when the quantity is invalid.
func Picoseconds(q Quantity) (int64, bool) {
	if !q.Valid {
		return 0, false
	}
	ps := q.Normalized * 1e12
	if ps < 0 {
		return 0, false
	}
	return int64(math.Round(ps)), true
}

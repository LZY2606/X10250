package ingest

import (
	"strconv"
	"strings"

	"edgebench/internal/domain"
)

// timeUnitFactor converts a unit to seconds; sample-rate unit to hertz.
var timeUnitFactor = map[string]float64{
	"s":  1,
	"ms": 1e-3,
	"us": 1e-6,
	"µs": 1e-6,
	"ns": 1e-9,
	"ps": 1e-12,
}

var rateUnitFactor = map[string]float64{
	"hz":  1,
	"khz": 1e3,
	"mhz": 1e6,
	"gs/s": 1e9,
	"ms/s": 1e6,
	"ks/s": 1e3,
	"s/s":  1,
}

// splitNumberUnit splits "12.5us" -> ("12.5", "us"). A bare number keeps unit "".
func splitNumberUnit(s string) (string, string) {
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

// ParseTimeQuantity preserves raw text/unit and normalizes to seconds.
func ParseTimeQuantity(raw, unit string) (domain.Quantity, bool) {
	q := domain.Quantity{Raw: raw, Unit: unit}
	num, u := splitNumberUnit(raw)
	if unit == "" {
		unit = u
		q.Unit = unit
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		q.Invalid = true
		return q, false
	}
	factor, ok := timeUnitFactor[strings.ToLower(unit)]
	if !ok {
		q.Invalid = true
		return q, false
	}
	q.Normalized = f * factor
	return q, true
}

// ParseRateQuantity preserves raw text/unit and normalizes to hertz.
func ParseRateQuantity(raw, unit string) (domain.Quantity, bool) {
	q := domain.Quantity{Raw: raw, Unit: unit}
	num, u := splitNumberUnit(raw)
	if unit == "" {
		unit = u
		q.Unit = unit
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		q.Invalid = true
		return q, false
	}
	factor, ok := rateUnitFactor[strings.ToLower(unit)]
	if !ok {
		q.Invalid = true
		return q, false
	}
	q.Normalized = f * factor
	return q, true
}

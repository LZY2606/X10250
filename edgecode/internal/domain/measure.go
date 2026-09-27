package domain

import (
	"math"
	"strconv"
	"strings"
)

// Known unit conversions to SI. Time accepts sec/ms/us/µs/ns/s; frequency
// accepts Sa/s, kSa/s, MSa/s, GSa/s (also sps variants). An unknown unit is
// not converted: the raw representation stays in place with a diagnostic.
var timeToS = map[string]float64{
	"s": 1, "sec": 1, "secs": 1, "second": 1, "seconds": 1,
	"ms": 1e-3, "us": 1e-6, "µs": 1e-6, "ns": 1e-9,
}

var freqToHz = map[string]float64{
	"sa/s": 1, "sps": 1, "samples/s": 1, "sample/s": 1,
	"ksa/s": 1e3, "msa/s": 1e6, "gsa/s": 1e9,
	"khz": 1e3, "mhz": 1e6, "ghz": 1e9, "hz": 1,
}

// ParseMeasure parses "<number> <unit>". Pos is attached to any diagnostic.
func ParseMeasure(raw, rawUnit string, pos Pos, kind string) Measure {
	m := Measure{Raw: raw, RawUnit: rawUnit}
	s := strings.TrimSpace(raw)
	if s == "" {
		m.Valid = false
		m.Diagnostic = &Diagnostic{Code: "measure.empty", Severity: SeverityError,
			Message: "empty numeric value", Pos: pos}
		return m
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		m.Valid = false
		m.Diagnostic = &Diagnostic{Code: "measure.not_a_number", Severity: SeverityError,
			Message: "cannot convert " + strconv.Quote(raw) + " to number", Pos: pos}
		return m
	}
	u := strings.ToLower(strings.TrimSpace(rawUnit))
	u = strings.ReplaceAll(u, "μ", "µ")
	u = strings.ReplaceAll(u, " ", "")
	var factor float64
	var siUnit string
	switch kind {
	case "time":
		factor, m.Valid = timeToS[u]
		siUnit = "s"
	case "freq":
		factor, m.Valid = freqToHz[u]
		siUnit = "Hz"
	default:
		factor, m.Valid = 1, true
		siUnit = rawUnit
	}
	if !m.Valid {
		m.SIV = 0
		m.Diagnostic = &Diagnostic{Code: "measure.unknown_unit", Severity: SeverityError,
			Message: "unknown unit " + strconv.Quote(rawUnit) + " for " + kind, Pos: pos}
		return m
	}
	m.SIV = f * factor
	m.SIUnit = siUnit
	m.Valid = true
	return m
}

// ParseLevel parses a raw logic level: 0/1, H/L, high/low. Anything else
// yields an unknown edge whose raw text is retained.
func ParseLevel(raw string, pos Pos, edgeSeq int) (*int, *Diagnostic) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "l", "low":
		v := 0
		return &v, nil
	case "1", "h", "high":
		v := 1
		return &v, nil
	}
	return nil, &Diagnostic{Code: "edge.bad_level", Severity: SeverityError,
		Message: "unconvertible logic level " + strconv.Quote(raw) + " (retained, treated unknown)",
		Pos: pos}
}

// ScaleSI formats an SI value with a sensible metric prefix for display.
func ScaleSI(v float64, unit string) string {
	pre := []struct{ p string; f float64 }{
		{"G", 1e9}, {"M", 1e6}, {"k", 1e3}, {"", 1}, {"m", 1e-3}, {"µ", 1e-6}, {"n", 1e-9},
	}
	for _, q := range pre {
		if math.Abs(v) >= q.f {
			return strconv.FormatFloat(v/q.f, 'f', 3, 64) + " " + q.p + unit
		}
	}
	return strconv.FormatFloat(v, 'f', 3, 64) + " " + unit
}

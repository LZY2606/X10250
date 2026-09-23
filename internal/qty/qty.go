// Package qty parses engineering quantities while preserving the original
// representation. A value that cannot be converted is returned in place
// with OK=false and a positioned diagnostic; callers must never pretend a
// parse failure succeeded.
package qty

import (
	"fmt"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// timeUnitFactors maps supported suffixes to seconds.
var timeUnitFactors = map[string]float64{
	"ns": 1e-9,
	"us": 1e-6, "µs": 1e-6,
	"ms": 1e-3,
	"s":  1,
}

var rateUnitFactors = map[string]float64{ // to Hz
	"hz":  1,
	"khz": 1e3,
	"mhz": 1e6,
}

// ParseTime parses "12.5us", "300ns", "0.000001s" or a unitless number
// interpreted as sample counts via rateHz (rateHz<=0 disables that form).
func ParseTime(raw string, line int, rateHz float64) (model.Quantity, []model.Diag) {
	q := model.Quantity{Raw: raw}
	s := strings.TrimSpace(raw)
	if s == "" {
		return q, []model.Diag{{Code: "time_empty", Severity: model.SevError, Line: line,
			Message: "empty time token", Kind: model.StatusCorrupt}}
	}
	num := s
	factor := 1.0
	unit := "s"
	for u, f := range timeUnitFactors {
		if strings.HasSuffix(s, u) {
			num = strings.TrimSpace(s[:len(s)-len(u)])
			factor = f
			unit = u
			goto conv
		}
	}
	{
		// unitless: sample count, requires a known rate
		lower := s
		if strings.HasSuffix(lower, "sa") || strings.HasSuffix(lower, "samples") {
			lower = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(lower, "sa"), "samples"))
		}
		if v, err := strconv.ParseFloat(lower, 64); err == nil {
			if rateHz <= 0 {
				return q, []model.Diag{{Code: "time_samples_no_rate", Severity: model.SevError, Line: line,
					Message: fmt.Sprintf("sample-count time %q needs @rate to convert", raw), Kind: model.StatusSemanticImpasse}}
			}
			q.Normalized = v / rateHz
			q.Unit = "s"
			q.OK = true
			return q, nil
		}
		return q, []model.Diag{{Code: "time_bad_unit", Severity: model.SevError, Line: line,
			Message: fmt.Sprintf("unparseable time token %q (want number + ns/us/ms/s, or sample count)", raw), Kind: model.StatusCorrupt}}
	}
conv:
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return q, []model.Diag{{Code: "time_bad_number", Severity: model.SevError, Line: line,
			Message: fmt.Sprintf("bad number in time token %q", raw), Kind: model.StatusCorrupt}}
	}
	if v < 0 {
		return q, []model.Diag{{Code: "time_negative", Severity: model.SevError, Line: line,
			Message: fmt.Sprintf("negative time %q", raw), Kind: model.StatusSemanticImpasse}}
	}
	q.Normalized = v * factor
	q.Unit = unit
	q.OK = true
	return q, nil
}

// ParseRate parses sample rates such as "25MHz" or "115200Hz".
func ParseRate(raw string, line int) (model.Quantity, float64, []model.Diag) {
	q := model.Quantity{Raw: raw}
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return q, 0, []model.Diag{{Code: "rate_empty", Severity: model.SevError, Line: line,
			Message: "empty sample rate", Kind: model.StatusCorrupt}}
	}
	for u, f := range rateUnitFactors {
		if strings.HasSuffix(s, u) {
			num := strings.TrimSpace(s[:len(s)-len(u)])
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return q, 0, []model.Diag{{Code: "rate_bad_number", Severity: model.SevError, Line: line,
					Message: fmt.Sprintf("bad number in rate %q", raw), Kind: model.StatusCorrupt}}
			}
			if v <= 0 {
				return q, 0, []model.Diag{{Code: "rate_nonpositive", Severity: model.SevError, Line: line,
					Message: fmt.Sprintf("sample rate must be > 0: %q", raw), Kind: model.StatusSemanticImpasse}}
			}
			q.Normalized = v * f
			q.Unit = "Hz"
			q.OK = true
			return q, q.Normalized, nil
		}
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
		q.Normalized = v
		q.Unit = "Hz"
		q.OK = true
		return q, v, nil
	}
	return q, 0, []model.Diag{{Code: "rate_bad_unit", Severity: model.SevError, Line: line,
		Message: fmt.Sprintf("unparseable sample rate %q (want e.g. 25MHz)", raw), Kind: model.StatusCorrupt}}
}

// ParsePolarity parses a declared polarity token without defaulting.
func ParsePolarity(raw string, allowed map[string]string) (string, []model.Diag) {
	k := strings.ToLower(strings.TrimSpace(raw))
	if v, ok := allowed[k]; ok {
		return v, nil
	}
	names := make([]string, 0, len(allowed))
	for k := range allowed {
		names = append(names, k)
	}
	return "", []model.Diag{{Code: "bad_polarity", Severity: model.SevError,
		Message: fmt.Sprintf("invalid polarity %q, allowed: %s", raw, strings.Join(names, ", ")), Kind: model.StatusCorrupt}}
}

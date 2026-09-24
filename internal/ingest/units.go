package ingest

import (
	"strconv"
	"strings"

	"edgebench/internal/domain"
)

// siScale normalizes common engineering units used by logic probes.
var siScale = map[string]float64{
	"hz": 1, "khz": 1e3, "mhz": 1e6, "ghz": 1e9,
	"s": 1e9, "ms": 1e6, "us": 1e3, "µs": 1e3, "ns": 1,
	"v": 1000, "mv": 1,
}

// ParsePhysical splits "100 MHz" / "1.65 V" into magnitude+unit.
func ParsePhysical(s string) (float64, string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, "", false
	}
	i := 0
	dot, digit := false, false
	for i < len(s) {
		r := s[i]
		if r >= '0' && r <= '9' {
			digit = true
		} else if r == '.' && !dot {
			dot = true
		} else if r == ' ' {
			break
		} else {
			return 0, "", false
		}
		i++
	}
	if !digit {
		return 0, "", false
	}
	mag, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0, "", false
	}
	unit := strings.TrimSpace(strings.ToLower(s[i:]))
	if unit == "" {
		return 0, "", false
	}
	return mag, unit, true
}

func isIntegral(f float64) (int64, bool) {
	v := int64(f)
	if f == float64(v) {
		return v, true
	}
	return 0, false
}

// TimeValue converts a physical time expression to nanoseconds.
func TimeValue(mag float64, unit string) (int64, bool) {
	scale, ok := siScale[unit]
	if !ok || scale < 1 { // time units only
		return 0, false
	}
	v := mag * scale
	return isIntegral(v)
}

// RateHz converts a frequency expression to hertz.
func RateHz(mag float64, unit string) (int64, bool) {
	scale, ok := siScale[unit]
	if !ok || scale < 1 || !(strings.Contains(unit, "hz")) {
		return 0, false
	}
	v := mag * scale
	return isIntegral(v)
}

// VoltMilli converts a voltage expression to millivolts.
func VoltMilli(mag float64, unit string) (int64, bool) {
	scale, ok := siScale[unit]
	if !ok || !(unit == "v" || unit == "mv") {
		return 0, false
	}
	v := mag * scale
	return isIntegral(v)
}

// RawInt builds domain.Raw from a physical quantity via conv.
func RawInt(text, unit string, mag float64, conv func(float64, string) (int64, bool)) (domain.Raw, *domain.Diagnostic) {
	out := domain.Raw{Text: text, Unit: unit}
	if v, ok := conv(mag, unit); ok {
		out.Value = &v
		return out, nil
	}
	return out, &domain.Diagnostic{
		Code: "value_not_integral", Severity: domain.SevWarn,
		Message: "value has no exact integer representation in the normalized unit; raw value retained",
	}
}

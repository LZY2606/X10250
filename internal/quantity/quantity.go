// Package quantity parses domain measurements while preserving the original
// representation. Times normalize to picoseconds, frequencies to millihertz,
// voltages to microvolts. Unconvertible values stay in place with a diagnostic.
package quantity

import (
	"regexp"
	"strconv"
	"strings"

	"edgebench/internal/domain"
)

var (
	numRe = regexp.MustCompile(`^\s*([+-]?\d+(?:\.\d+)?)(?:[eE]([+-]?\d+))?\s*(\S*)\s*$`)
)

func fail(raw, unit, pos, msg string) (domain.Quantity, *domain.Diagnostic) {
	d := &domain.Diagnostic{Code: "UNCONVERTIBLE_VALUE", Severity: "error", Position: pos, Message: msg}
	return domain.Quantity{Raw: raw, Unit: unit, Valid: false, Diagnostic: d}, d
}

// ParseTime accepts ps/ns/us/µs/ms/s suffixes.
func ParseTime(raw string) (domain.Quantity, *domain.Diagnostic) {
	q := domain.Quantity{Raw: strings.TrimSpace(raw)}
	m := numRe.FindStringSubmatch(raw)
	if m == nil {
		return fail(raw, "", "", "time not numeric")
	}
	unit := m[3]
	q.Unit = unit
	mult, ok := map[string]int64{
		"ps": 1, "ns": 1_000, "us": 1_000_000, "µs": 1_000_000,
		"ms": 1_000_000_000, "s": 1_000_000_000_000, "": -1,
	}[unit]
	if !ok {
		return fail(raw, unit, "", "unknown time unit: "+unit)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fail(raw, unit, "", err.Error())
	}
	if m[2] != "" {
		e, _ := strconv.Atoi(m[2])
		v *= pow10f(e)
	}
	if unit == "" {
		// Bare numbers are accepted only with an explicit position hint from
		// callers; default unit is samples-independent picoseconds.
		mult = 1
	}
	ps := int64(0)
	if v >= 0 {
		ps = int64(v*float64(mult) + 0.5)
	} else {
		ps = int64(v*float64(mult) - 0.5)
	}
	q.Valid = true
	q.NormPs = ps
	return q, nil
}

func ParseRate(raw string) (domain.Quantity, *domain.Diagnostic) {
	m := numRe.FindStringSubmatch(raw)
	if m == nil {
		return fail(raw, "", "", "sample rate not numeric")
	}
	unit := m[3]
	milliMult, ok := map[string]int64{
		"mHz": 1, "Hz": 1_000, "kHz": 1_000_000,
		"MHz": 1_000_000_000, "GHz": 1_000_000_000_000,
	}[unit]
	if !ok {
		return fail(raw, unit, "", "unknown frequency unit: "+unit)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fail(raw, unit, "", err.Error())
	}
	if m[2] != "" {
		e, _ := strconv.Atoi(m[2])
		v *= pow10f(e)
	}
	q := domain.Quantity{Raw: strings.TrimSpace(raw), Unit: unit, Valid: true, NormMilliHz: int64(v*float64(milliMult) + 0.5)}
	return q, nil
}

func ParseVoltage(raw string) (domain.Quantity, *domain.Diagnostic) {
	m := numRe.FindStringSubmatch(raw)
	if m == nil {
		return fail(raw, "", "", "threshold not numeric")
	}
	unit := m[3]
	microMult, ok := map[string]int64{"uV": 1, "µV": 1, "mV": 1_000, "V": 1_000_000}[unit]
	if !ok {
		return fail(raw, unit, "", "unknown voltage unit: "+unit)
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return fail(raw, unit, "", err.Error())
	}
	if m[2] != "" {
		e, _ := strconv.Atoi(m[2])
		v *= pow10f(e)
	}
	q := domain.Quantity{Raw: strings.TrimSpace(raw), Unit: unit, Valid: true, NormMicroV: int64(v*float64(microMult) + 0.5)}
	return q, nil
}

func pow10f(e int) float64 {
	r := 1.0
	if e >= 0 {
		for i := 0; i < e; i++ {
			r *= 10
		}
	} else {
		for i := 0; i < -e; i++ {
			r /= 10
		}
	}
	return r
}

// FormatPs renders picoseconds with an appropriate unit for display.
func FormatPs(ps int64) string {
	neg := ps < 0
	if neg {
		ps = -ps
	}
	switch {
	case ps >= 1_000_000_000_000 && ps%1_000_000_000_000 == 0:
		return sign(neg) + strconv.FormatInt(ps/1_000_000_000_000, 10) + "s"
	case ps >= 1_000_000_000 && ps%1_000_000_000 == 0:
		return sign(neg) + strconv.FormatInt(ps/1_000_000_000, 10) + "ms"
	case ps >= 1_000_000 && ps%1_000_000 == 0:
		return sign(neg) + strconv.FormatInt(ps/1_000_000, 10) + "us"
	case ps >= 1_000 && ps%1_000 == 0:
		return sign(neg) + strconv.FormatInt(ps/1_000, 10) + "ns"
	default:
		return sign(neg) + strconv.FormatInt(ps, 10) + "ps"
	}
}

func sign(neg bool) string {
	if neg {
		return "-"
	}
	return ""
}

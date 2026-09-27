// Package units keeps physical quantities in their original representation and
// derives normalized values without discarding the source form.
package units

import (
	"math"
	"strconv"
	"strings"
)

// TimePoint is a time value as written in the source material.
type TimePoint struct {
	Raw    string `json:"raw"`
	Unit   string `json:"unit"`
	Nanos  int64  `json:"nanos,omitempty"`
	Parsed bool   `json:"parsed"`
}

// Frequency is a rate as written in the source material (samples/s, Hz).
type Frequency struct {
	Raw    string `json:"raw"`
	Unit   string `json:"unit"`
	Hz     int64  `json:"hz,omitempty"`
	Parsed bool   `json:"parsed"`
}

var timeFactors = map[string]int64{
	"ns": 1,
	"us": 1_000,
	"µs": 1_000,
	"ms": 1_000_000,
	"s":  1_000_000_000,
}

// ParseTime accepts numbers with explicit time units. A value that cannot be
// converted stays at its original position with Parsed=false.
func ParseTime(raw interface{}) TimePoint {
	s, unit, ok := splitNumberUnit(raw)
	if !ok {
		return TimePoint{Raw: asText(raw), Parsed: false}
	}
	f, ok := parseFloat(s)
	if !ok {
		return TimePoint{Raw: asText(raw), Unit: unit, Parsed: false}
	}
	factor, known := timeFactors[unit]
	if !known {
		return TimePoint{Raw: asText(raw), Unit: unit, Parsed: false}
	}
	return TimePoint{Raw: asText(raw), Unit: unit, Nanos: int64(math.Round(f * float64(factor))), Parsed: true}
}

var rateFactors = map[string]int64{
	"hz":     1,
	"khz":    1_000,
	"mhz":    1_000_000,
	"sps":    1,
	"ksps":   1_000,
	"msps":   1_000_000,
	"sa/s":   1,
	"ksa/s":  1_000,
	"msa/s":  1_000_000,
	"sample/s":  1,
	"ksample/s": 1_000,
	"msample/s": 1_000_000,
}

// ParseRate parses a sampling rate such as "20 MHz" / "100 ksps".
func ParseRate(raw interface{}) Frequency {
	txt := asText(raw)
	s, unit, ok := splitNumberUnit(raw)
	if !ok {
		return Frequency{Raw: txt, Parsed: false}
	}
	f, ok := parseFloat(s)
	if !ok {
		return Frequency{Raw: txt, Unit: unit, Parsed: false}
	}
	factor, known := rateFactors[strings.ToLower(unit)]
	if !known {
		return Frequency{Raw: txt, Unit: unit, Parsed: false}
	}
	return Frequency{Raw: txt, Unit: unit, Hz: int64(math.Round(f * float64(factor))), Parsed: true}
}

func splitNumberUnit(raw interface{}) (num, unit string, ok bool) {
	txt := strings.TrimSpace(asText(raw))
	if txt == "" {
		return "", "", false
	}
	i := 0
	dotSeen := false
	for i < len(txt) {
		c := txt[i]
		if c >= '0' && c <= '9' {
			i++
			continue
		}
		if (c == '-' || c == '+') && i == 0 {
			i++
			continue
		}
		if c == '.' && !dotSeen {
			dotSeen = true
			i++
			continue
		}
		if c == 'e' || c == 'E' {
			i++
			if i < len(txt) && (txt[i] == '+' || txt[i] == '-') {
				i++
			}
			continue
		}
		break
	}
	if i == 0 {
		return "", "", false
	}
	return txt[:i], strings.TrimSpace(txt[i:]), true
}

func parseFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

func asText(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 64)
	case nil:
		return ""
	default:
		return ""
	}
}

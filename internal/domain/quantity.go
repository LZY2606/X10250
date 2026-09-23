package domain

import (
	"regexp"
	"strconv"
	"strings"
)

// Unit scales to nanoseconds for time quantities.
var timeUnitNS = map[string]float64{
	"ns": 1, "us": 1e3, "µs": 1e3, "ms": 1e6, "s": 1e9,
}

// Sample-rate tokens normalize to samples per second.
var rateUnitSPS = map[string]float64{
	"hz": 1, "khz": 1e3, "mhz": 1e6, "ghz": 1e9,
	"sa/s": 1, "ksa/s": 1e3, "msa/s": 1e6, "gsa/s": 1e9,
	"s/s": 1, "ks/s": 1e3, "ms/s": 1e6, "gs/s": 1e9,
}

var qre = regexp.MustCompile(`^\s*([-+]?[0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*([^0-9\s].*?)?\s*$`)

// ParseTime parses a time token relative to the capture time unit. The
// original representation is always retained; an unconvertible token returns
// Quantity{Unconvertible:true} instead of an invented value.
func ParseTime(tok, captureUnit string) (Quantity, bool) {
	q, ok := parseNumberUnit(tok)
	if !ok {
		return Quantity{Original: tok, Unconvertible: true}, false
	}
	unit := q.Unit
	if unit == "" {
		unit = captureUnit
	}
	scale, known := timeUnitNS[strings.ToLower(unit)]
	if !known {
		q.Original = tok
		q.Unconvertible = true
		q.Value = 0
		q.Unit = unit
		return q, false
	}
	if q.Unit == "" {
		q.Unit = captureUnit
	}
	q.Value *= scale
	q.Unit = "ns"
	return q, true
}

// ParseSampleRate parses sample-rate tokens like "5 MS/s" or "500kHz".
func ParseSampleRate(tok string) (Quantity, bool) {
	q, ok := parseNumberUnit(tok)
	if !ok {
		return Quantity{Original: tok, Unconvertible: true}, false
	}
	unit := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(q.Unit)), " ", "")
	if unit == "" {
		q.Original = tok
		q.Unconvertible = true
		return q, false
	}
	scale, known := rateUnitSPS[unit]
	if !known {
		q.Original = tok
		q.Unconvertible = true
		return q, false
	}
	q.Value *= scale
	q.Unit = "S/s"
	return q, true
}

func parseNumberUnit(tok string) (Quantity, bool) {
	m := qre.FindStringSubmatch(strings.TrimSpace(tok))
	if m == nil {
		return Quantity{Original: tok}, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return Quantity{Original: tok}, false
	}
	return Quantity{Original: strings.TrimSpace(tok), Value: v, Unit: strings.TrimSpace(m[2])}, true
}

// ParseLevel normalizes a level token against an inversion flag, retaining the
// raw token. Returns level, normalized token, ok.
func ParseLevel(raw string, inverted bool) (int, string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "l", "low":
		l := 0
		if inverted {
			l = 1
		}
		return l, "0", true
	case "1", "h", "high":
		l := 1
		if inverted {
			l = 0
		}
		return l, "1", true
	}
	return 0, "", false
}

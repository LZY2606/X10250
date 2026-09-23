package edgelog

import (
	"regexp"
	"strconv"
	"strings"
)

// timeMultipliers converts a unit suffix to seconds.
var timeMultipliers = map[string]float64{
	"ps": 1e-12,
	"ns": 1e-9,
	"us": 1e-6,
	"µs": 1e-6,
	"ms": 1e-3,
	"s":  1,
}

// freqMultipliers converts a rate unit to hertz.
var freqMultipliers = map[string]float64{
	"hz":   1,
	"khz":  1e3,
	"mhz":  1e6,
	"ghz":  1e9,
	"sps":  1,
	"ks/s": 1e3,
	"ms/s": 1e6,
	"gs/s": 1e9,
}

var qtyRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*([A-Za-zµΩ/]+)?$`)

// ParseTime parses a time token to seconds. ok=false means the value must
// remain in place; the original representation is never dropped.
func ParseTime(token string) (sec float64, unit string, ok bool) {
	m := qtyRe.FindStringSubmatch(strings.TrimSpace(token))
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", false
	}
	u := strings.ToLower(m[2])
	if u == "" {
		return v, "", true
	}
	mult, found := timeMultipliers[u]
	if !found {
		return 0, m[2], false
	}
	return v * mult, m[2], true
}

// ParseRate parses a sample-rate token to hertz.
func ParseRate(token string) (hz float64, unit string, ok bool) {
	m := qtyRe.FindStringSubmatch(strings.TrimSpace(token))
	if m == nil {
		return 0, "", false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", false
	}
	u := strings.ToLower(m[2])
	if u == "" {
		return v, "", true
	}
	mult, found := freqMultipliers[u]
	if !found {
		return 0, m[2], false
	}
	return v * mult, m[2], true
}

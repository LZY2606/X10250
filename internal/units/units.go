// Package units parses physical quantities while preserving the original
// text. Normalization happens only at the derived layer; a value that cannot
// be converted stays in place with OK=false and an explanation.
package units

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

var (
	numRe   = regexp.MustCompile(`^[ \t]*([+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)[ \t]*(.*)$`)
	clockRe = regexp.MustCompile(`^[ \t]*(\d+)[ \t]*$`)
)

// timeUnits: unit token -> multiplier to seconds.
var timeUnits = map[string]float64{
	"":     1.0,
	"s":    1.0,
	"sec":  1.0,
	"secs": 1.0,
	"ms":   1e-3,
	"us":   1e-6,
	"µs":   1e-6,
	"ns":   1e-9,
	"ps":   1e-12,
}

// freqUnits: unit token -> multiplier to hertz.
var freqUnits = map[string]float64{
	"":  1.0,
	"hz":  1.0,
	"khz": 1e3,
	"mhz": 1e6,
	"ghz": 1e9,
}

func ParseTime(text string) model.Orig {
	o := model.Orig{Text: strings.TrimSpace(text)}
	m := numRe.FindStringSubmatch(text)
	if m == nil {
		o.Diag = "cannot parse time expression"
		return o
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		o.Diag = "cannot parse numeric part"
		return o
	}
	unit := strings.ToLower(strings.TrimSpace(m[2]))
	mul, ok := timeUnits[unit]
	if !ok {
		o.Unit = unit
		o.Diag = fmt.Sprintf("unknown time unit %q", unit)
		return o
	}
	o.Unit = unit
	o.SI = v * mul
	o.OK = true
	return o
}

func ParseFrequency(text string) model.Orig {
	o := model.Orig{Text: strings.TrimSpace(text)}
	m := numRe.FindStringSubmatch(text)
	if m == nil {
		o.Diag = "cannot parse frequency expression"
		return o
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		o.Diag = "cannot parse numeric part"
		return o
	}
	unit := strings.ToLower(strings.TrimSpace(m[2]))
	mul, ok := freqUnits[unit]
	if !ok {
		o.Unit = unit
		o.Diag = fmt.Sprintf("unknown frequency unit %q", unit)
		return o
	}
	o.Unit = unit
	o.SI = v * mul
	o.OK = true
	return o
}

// ParseClockPolarity maps free-form polarity text to a normalized token.
// Accepted: active-high / rising / idle-low / "1" (=> idle-low/rising),
// active-low / falling / idle-high / "0".
func ParseClockPolarity(text string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "1", "active-high", "high", "rising", "idle-low", "pos":
		return "idle-low", true
	case "0", "active-low", "low", "falling", "idle-high", "neg":
		return "idle-high", true
	}
	return "", false
}

// ParseSampleEdge maps free-form sampling edge text.
func ParseSampleEdge(text string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "rising", "rise", "pos", "leading":
		return "rising", true
	case "falling", "fall", "neg", "trailing":
		return "falling", true
	}
	return "", false
}

// ParseBoolOrig retains the original inversion token.
func ParseBoolOrig(text string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "1", "yes", "y", "inv", "inverted", "high":
		return true, true
	case "false", "0", "no", "n", "normal", "low":
		return false, true
	}
	return false, false
}

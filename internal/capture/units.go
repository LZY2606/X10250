package capture

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"edgetrace/internal/model"
)

// siPrefix maps SI unit prefixes to their decimal exponent.
var siPrefix = map[string]int{
	"": 0, "k": 3, "K": 3, "M": 6, "G": 9,
	"m": -3, "u": -6, "µ": -6, "n": -9, "p": -12,
}

var rateRE = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([kKMGTmunpµ]?)(Hz)$`)
var timeRE = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([munpµ]?)(s)$`)

// parseRate converts a sample-rate expression to an exact integer Hz value.
// Non-integral rates are rejected (no fake default).
func parseRate(text string) (int64, error) {
	m := rateRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return 0, fmt.Errorf("unparseable sample rate %q", text)
	}
	exp := siPrefix[m[2]]
	intPart, fracPart := splitNumber(m[1])
	digits := len(fracPart)
	v, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return 0, err
	}
	exp -= digits
	if exp < 0 {
		return 0, fmt.Errorf("sample rate %q is not an integral number of Hz", text)
	}
	for exp > 0 {
		v *= 10
		exp--
	}
	return v, nil
}

func splitNumber(s string) (string, string) {
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// parseTime converts an offset expression to an exact rational nanoseconds.
func parseTime(text string) (model.Rat, error) {
	m := timeRE.FindStringSubmatch(strings.TrimSpace(text))
	if m == nil {
		return model.Rat{}, fmt.Errorf("unparseable time offset %q", text)
	}
	exp := siPrefix[m[2]] - 9 // target unit is nanoseconds
	intPart, fracPart := splitNumber(m[1])
	digits := len(fracPart)
	v, err := strconv.ParseInt(intPart+fracPart, 10, 64)
	if err != nil {
		return model.Rat{}, err
	}
	exp -= digits
	switch {
	case exp >= 0:
		for exp > 0 {
			v *= 10
			exp--
		}
		return model.RatInt(v), nil
	default:
		return model.NewRat(v, pow10(-exp))
	}
}

func pow10(n int) int64 {
	v := int64(1)
	for i := 0; i < n; i++ {
		v *= 10
	}
	return v
}

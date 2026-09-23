package capture

import (
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// parseTimeNS converts tokens like "1500ns", "12us", "2.5ms" to integer ns.
// Values that cannot be converted return ok=false and stay at source.
func parseTimeNS(tok string) (int64, bool) {
	units := []struct {
		suf string
		mul float64
	}{
		{"ns", 1}, {"us", 1e3}, {"µs", 1e3}, {"ms", 1e6}, {"s", 1e9},
	}
	for _, u := range units {
		if strings.HasSuffix(tok, u.suf) {
			num := strings.TrimSuffix(tok, u.suf)
			v, err := strconv.ParseFloat(num, 64)
			if err != nil || v < 0 {
				return 0, false
			}
			return int64(v*u.mul + 0.5), true
		}
	}
	return 0, false
}

func parseLevel(tok string) (int, bool) {
	switch tok {
	case "0", "L", "low":
		return 0, true
	case "1", "H", "high":
		return 1, true
	}
	return -1, false
}

// parseRate accepts 1MHz/500kHz/1000000Hz style tokens.
func parseRate(tok string) model.SampleRate {
	r := model.SampleRate{Raw: tok}
	units := []struct {
		suf string
		mul float64
	}{
		{"GHz", 1e9}, {"MHz", 1e6}, {"kHz", 1e3}, {"Hz", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(tok, u.suf) {
			v, err := strconv.ParseFloat(strings.TrimSuffix(tok, u.suf), 64)
			if err != nil || v <= 0 {
				return r
			}
			hz := int64(v*u.mul + 0.5)
			r.Hz, r.UnitOK, r.PeriodNS = hz, true, int64(1e9/float64(hz)+0.5)
			return r
		}
	}
	return r
}

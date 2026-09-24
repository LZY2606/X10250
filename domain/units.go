package domain

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Multipliers to nanoseconds and to hertz. Unknown units stay unconverted.
var timeToNS = map[string]float64{
	"ps": 1e-3,
	"ns": 1,
	"us": 1e3, "µs": 1e3,
	"ms": 1e6,
	"s":  1e9,
}

var unitToHz = map[string]float64{
	"hz": 1, "khz": 1e3, "mhz": 1e6, "ghz": 1e9,
}

func splitMagnitudeUnit(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
}

// ParseTime converts one time expression ("12.5us", 3 numbers, "100 ns") to
// nanoseconds. Unknown units or bad numbers yield Ok=false diagnostics and no
// fabricated value.
func ParseTime(raw, unit string, pos Pos) Q {
	mag, u := splitMagnitudeUnit(raw)
	if u == "" {
		u = unit
	}
	q := Q{Raw: raw, Unit: u}
	m, err := strconv.ParseFloat(mag, 64)
	if err != nil {
		q.Diagnostics = []Diagnostic{{Code: "time_bad_number", Severity: SevError, Pos: pos,
			Message: "time magnitude is not numeric: " + mag}}
		return q
	}
	mult, ok := timeToNS[strings.ToLower(u)]
	if !ok {
		q.Diagnostics = []Diagnostic{{Code: "time_unknown_unit", Severity: SevError, Pos: pos,
			Message: "unknown time unit: " + u}}
		return q
	}
	q.Value = m * mult
	q.Ok = true
	return q
}

// ParseRate converts a sample rate ("25MHz", 1000, "100 kHz") to hertz.
func ParseRate(raw, unit string, pos Pos) Q {
	mag, u := splitMagnitudeUnit(raw)
	if u == "" {
		u = unit
	}
	q := Q{Raw: raw, Unit: u}
	m, err := strconv.ParseFloat(mag, 64)
	if err != nil {
		q.Diagnostics = []Diagnostic{{Code: "rate_bad_number", Severity: SevError, Pos: pos,
			Message: "sample rate is not numeric: " + mag}}
		return q
	}
	mult, ok := unitToHz[strings.ToLower(u)]
	if !ok {
		q.Diagnostics = []Diagnostic{{Code: "rate_unknown_unit", Severity: SevError, Pos: pos,
			Message: "unknown frequency unit: " + u}}
		return q
	}
	q.Value = m * mult
	q.Ok = true
	return q
}

// ParseRawTime accepts the supported edge-time shapes:
//   - number: measured in the capture default time unit
//   - string: magnitude+unit ("12.5us")
//   - object: {"t": "..", "unit": ".."} or {"sample": 128}
// The original representation is always kept. Sample indices are converted via
// the sample rate; without a valid rate they remain non-orderable.
func ParseRawTime(v json.RawMessage, defaultUnit string, rate Q, pos Pos) RawTime {
	rt := RawTime{}
	var n json.Number
	if err := json.Unmarshal(v, &n); err == nil && n.String() != "" {
		rt.Kind, rt.Raw, rt.Unit = "time", n.String(), defaultUnit
		q := ParseTime(n.String(), defaultUnit, pos)
		rt.Diagnostics = q.Diagnostics
		if q.Ok {
			ns := int64(math.Round(q.Value))
			rt.NS, rt.Orderable = &ns, true
		}
		return rt
	}
	var s string
	if err := json.Unmarshal(v, &s); err == nil && s != "" {
		rt.Kind, rt.Raw = "time", s
		q := ParseTime(s, defaultUnit, pos)
		rt.Unit, rt.Diagnostics = q.Unit, q.Diagnostics
		if q.Ok {
			ns := int64(math.Round(q.Value))
			rt.NS, rt.Orderable = &ns, true
		}
		return rt
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(v, &obj); err == nil {
		if sv, ok := obj["sample"]; ok {
			rt.Kind, rt.Unit = "sample", rate.Raw
			var idx int64
			if err := json.Unmarshal(sv, &idx); err != nil {
				rt.Raw = string(sv)
				rt.Diagnostics = []Diagnostic{{Code: "time_bad_sample", Severity: SevError, Pos: pos.At("sample"),
					Message: "sample index is not an integer"}}
				return rt
			}
			rt.Sample, rt.Raw = idx, strconv.FormatInt(idx, 10)
			if rate.Ok {
				ns := int64(math.Round(float64(idx) / rate.Value * 1e9))
				rt.NS, rt.Orderable = &ns, true
			} else {
				rt.Diagnostics = []Diagnostic{{Code: "time_sample_without_rate", Severity: SevError, Pos: pos,
					Message: "sample-index time cannot be ordered: sample rate is not convertible"}}
			}
			return rt
		}
		if tv, ok := obj["t"]; ok {
			var ts string
			if err := json.Unmarshal(tv, &ts); err != nil {
				rt.Raw = string(tv)
				rt.Diagnostics = []Diagnostic{{Code: "time_bad_value", Severity: SevError, Pos: pos.At("t"),
					Message: "time value is not a string"}}
				return rt
			}
			u := defaultUnit
			if uv, ok2 := obj["unit"]; ok2 {
				_ = json.Unmarshal(uv, &u)
			}
			rt.Kind, rt.Raw, rt.Unit = "time", ts, u
			q := ParseTime(ts, u, pos)
			rt.Diagnostics = q.Diagnostics
			if q.Ok {
				ns := int64(math.Round(q.Value))
				rt.NS, rt.Orderable = &ns, true
			}
			return rt
		}
	}
	rt.Raw = string(v)
	rt.Diagnostics = []Diagnostic{{Code: "time_bad_shape", Severity: SevError, Pos: pos,
		Message: "edge time must be a number, string, {'t':..,'unit':..} or {'sample':..}"}}
	return rt
}

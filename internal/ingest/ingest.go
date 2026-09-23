// Package ingest parses raw digital-capture material into the immutable layer.
// Original representations are always preserved; normalization is derived.
package ingest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

const FormatVersion = "edgebench.capture/v1"

// Err classes — resource, format and semantic failures are distinct.
type ErrCode string

const (
	ErrFormatCorrupt   ErrCode = "FORMAT_CORRUPT"
	ErrSemanticNoSolve ErrCode = "SEMANTIC_NO_SOLUTION"
	ErrBudgetExhausted ErrCode = "BUDGET_EXHAUSTED"
)

// Error carries a stable code and positioned diagnostics.
type Error struct {
	Code        ErrCode
	Message     string
	Diagnostics []model.Diagnostic
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type rawCapture struct {
	FormatVersion string     `json:"format_version"`
	SourceFile    string     `json:"source_file"`
	CapturedAt    string     `json:"captured_at"`
	SampleRate    rawQty     `json:"sample_rate"`
	Probes        []rawProbe `json:"probes"`
	Edges         []rawEdge  `json:"edges"`
}

type rawQty struct {
	Raw   string      `json:"raw"`
	Unit  string      `json:"unit"`
	Basis string      `json:"basis"`
	V     json.Number `json:"v"`
}

type rawProbe struct {
	Channel     string `json:"channel"`
	Alias       string `json:"alias"`
	InvertedRaw string `json:"inverted"`
	Role        string `json:"role"`
	ClkPolRaw   string `json:"clk_polarity"`
	Volt        rawQty `json:"volt"`
}

type rawEdge struct {
	T     rawQty `json:"t"`
	Level string `json:"level"`
	Chan  string `json:"chan"`
}

// toQty converts a raw quantity, keeping unconvertible values in place.
func toQty(q rawQty, path string, diags *[]model.Diagnostic) model.Qty {
	out := model.Qty{Raw: q.Raw, Unit: q.Unit, Basis: q.Basis}
	raw := strings.TrimSpace(q.Raw)
	switch {
	case q.V != "":
		if f, err := q.V.Float64(); err == nil {
			out.V = f
			out.Ok = true
			return out
		}
	case raw != "":
		if f, err := parseNumberWithUnit(raw); err == nil {
			out.V = f
			out.Ok = true
			if out.Unit == "" {
				out.Unit = unitOf(raw)
			}
			return out
		}
	}
	if raw == "" && q.V == "" {
		*diags = append(*diags, model.Diagnostic{
			Code: "QTY_MISSING", Severity: model.SevError,
			Message: "quantity has neither raw text nor numeric value", Pos: model.Pos{Path: path},
		})
		return out
	}
	*diags = append(*diags, model.Diagnostic{
		Code: "QTY_UNCONVERTIBLE", Severity: model.SevWarning,
		Message: "value cannot be normalized; raw representation kept in place",
		Pos:     model.Pos{Path: path}, Value: raw,
	})
	return out
}

var unitMult = map[string]float64{
	"s": 1, "ms": 1e-3, "us": 1e-6, "µs": 1e-6, "ns": 1e-9, "ps": 1e-12,
	"hz": 1, "khz": 1e3, "mhz": 1e6, "ghz": 1e9,
	"v": 1, "mv": 1e-3,
}

func unitOf(s string) string {
	for _, u := range []string{"ms", "us", "µs", "ns", "ps", "s"} {
		if strings.HasSuffix(s, u) {
			return u
		}
	}
	return ""
}

func parseNumberWithUnit(s string) (float64, error) {
	s = strings.TrimSpace(s)
	// longest unit suffix first
	for _, u := range []string{"mhz", "khz", "ghz", "hz", "ms", "us", "µs", "ns", "ps", "mv", "s", "v"} {
		if strings.HasSuffix(strings.ToLower(s), u) {
			num := strings.TrimSpace(s[:len(s)-len(u)])
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, err
			}
			return f * unitMult[u], nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	return f, nil
}

func parseBoolRaw(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "high", "inverted":
		return true, true
	case "false", "0", "no", "low", "normal":
		return false, true
	}
	return false, false
}

// Parse decodes capture JSON. Format corruption, semantic impossibility and
// resource exhaustion are reported as distinct outcomes; partial successes
// carry diagnostics instead of hiding behind defaults.
func Parse(data []byte, maxEdges int) (model.Capture, error) {
	var rc rawCapture
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&rc); err != nil {
		return model.Capture{}, &Error{Code: ErrFormatCorrupt, Message: "invalid JSON: " + err.Error()}
	}
	if rc.FormatVersion == "" {
		return model.Capture{}, &Error{Code: ErrFormatCorrupt, Message: "missing format_version"}
	}
	if rc.FormatVersion != FormatVersion {
		return model.Capture{}, &Error{Code: ErrFormatCorrupt,
			Message: fmt.Sprintf("unsupported format_version %q", rc.FormatVersion)}
	}
	var diags []model.Diagnostic
	cap0 := model.Capture{
		FormatVersion: rc.FormatVersion,
		SourceFile:    rc.SourceFile,
		CapturedAt:    rc.CapturedAt,
	}
	cap0.SampleRate = toQty(rc.SampleRate, "sample_rate", &diags)
	if cap0.SampleRate.Ok && cap0.SampleRate.Unit != "" && !isRateUnit(cap0.SampleRate.Unit) {
		diags = append(diags, model.Diagnostic{
			Code: "SR_UNIT_BAD", Severity: model.SevError,
			Message: "sample_rate unit is not a frequency unit", Pos: model.Pos{Path: "sample_rate.unit"},
			Value: cap0.SampleRate.Unit,
		})
	}
	known := map[string]bool{}
	for i, rp := range rc.Probes {
		p := model.Probe{
			Channel: rp.Channel, Alias: rp.Alias, InvertedRaw: rp.InvertedRaw,
			Role: rp.Role, ClkPolRaw: rp.ClkPolRaw,
			Volt: toQty(rp.Volt, fmt.Sprintf("probes[%d].volt", i), &diags),
		}
		if v, ok := parseBoolRaw(rp.InvertedRaw); ok {
			p.Inverted = v
		} else if rp.InvertedRaw != "" {
			diags = append(diags, model.Diagnostic{
				Code: "INVERT_BAD", Severity: model.SevWarning,
				Message: "inverted flag unparseable, raw kept", Pos: model.Pos{Path: fmt.Sprintf("probes[%d].inverted", i)},
				Value: rp.InvertedRaw,
			})
		}
		if rp.ClkPolRaw != "" && rp.ClkPolRaw != "0" && rp.ClkPolRaw != "1" &&
			strings.ToLower(rp.ClkPolRaw) != "low" && strings.ToLower(rp.ClkPolRaw) != "high" {
			diags = append(diags, model.Diagnostic{
				Code: "CLKPOL_BAD", Severity: model.SevWarning,
				Message: "clock polarity not 0/1/low/high; kept raw, decoder keeps candidates",
				Pos:     model.Pos{Path: fmt.Sprintf("probes[%d].clk_polarity", i)}, Value: rp.ClkPolRaw,
			})
		}
		if rp.Alias == "" {
			p.Alias = rp.Channel
		}
		if p.Channel == "" {
			diags = append(diags, model.Diagnostic{Code: "PROBE_NO_CHANNEL", Severity: model.SevError,
				Message: "probe missing channel", Pos: model.Pos{Path: fmt.Sprintf("probes[%d]", i)}})
		} else if known[p.Channel] {
			diags = append(diags, model.Diagnostic{Code: "PROBE_DUP", Severity: model.SevError,
				Message: "duplicate probe channel", Pos: model.Pos{Path: fmt.Sprintf("probes[%d].channel", i)},
				Value: p.Channel})
		}
		known[p.Channel] = true
		cap0.Probes = append(cap0.Probes, p)
	}
	if maxEdges > 0 && len(rc.Edges) > maxEdges {
		return cap0, &Error{Code: ErrBudgetExhausted,
			Message: fmt.Sprintf("edge budget: %d edges exceeds limit %d", len(rc.Edges), maxEdges)}
	}
	anchor := cap0.SourceFile
	if anchor == "" {
		anchor = "capture"
	}
	seenAlias := map[string]int{}
	for i, re := range rc.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		e := model.Edge{Chan: re.Chan, Level: re.Level, T: toQty(re.T, path+".t", &diags)}
		if re.Level != "0" && re.Level != "1" {
			diags = append(diags, model.Diagnostic{Code: "EDGE_LEVEL_BAD", Severity: model.SevError,
				Message: "edge level must be \"0\" or \"1\"", Pos: model.Pos{Path: path + ".level"},
				Value: re.Level})
		}
		if !known[re.Chan] {
			diags = append(diags, model.Diagnostic{Code: "EDGE_CHAN_UNKNOWN", Severity: model.SevError,
				Message: "edge on channel without probe", Pos: model.Pos{Path: path + ".chan"},
				Value: re.Chan})
		}
		e.ID = fmt.Sprintf("E-%s-%d", hash8(anchor), seenAlias[re.Chan])
		seenAlias[re.Chan]++
		cap0.Edges = append(cap0.Edges, e)
	}
	cap0.Diagnostics = diags
	// Structural semantic failures only; unconvertible quantities stay as warnings.
	if n := countSeverity(diags, model.SevError); n > 0 {
		return cap0, &Error{Code: ErrSemanticNoSolve, Diagnostics: diags,
			Message: fmt.Sprintf("%d semantic problem(s) in capture", n)}
	}
	return cap0, nil
}

func countSeverity(ds []model.Diagnostic, s model.Severity) int {
	n := 0
	for _, d := range ds {
		if d.Severity == s {
			n++
		}
	}
	return n
}

func isRateUnit(u string) bool {
	switch strings.ToLower(u) {
	case "hz", "khz", "mhz", "ghz":
		return true
	}
	return false
}

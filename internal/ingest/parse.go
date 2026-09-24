// Package ingest turns a raw digital-capture document into an immutable
// model.Capture. Original representations (time tokens, rate tokens, aliases,
// inversion flags) are preserved verbatim; normalization happens only in the
// derived Quantity.Value field.
package ingest

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"edgebench/internal/model"
)

// Budget limits. Exceeding a limit is a distinct outcome (ClassBudget), never
// a generic error.
const (
	MaxEdges       = 200000
	MaxProbes      = 64
	MaxSourceBytes = 8 << 20
)

// CaptureDoc is the on-disk/offline case document. Numbers for times are given
// as strings so the original representation survives ("1.00us" is not the same
// provenance as 1e-6).
type CaptureDoc struct {
	PackageVersion string    `json:"packageVersion"`
	Kind           string    `json:"kind"`
	SourceName     string    `json:"sourceName"`
	SampleRate     string    `json:"sampleRate"`
	TimeBase       string    `json:"timeBase"`
	Probes         []ProbeDoc `json:"probes"`
	Edges          []EdgeDoc `json:"edges"`
	Notes          string    `json:"notes"`
}

type ProbeDoc struct {
	Channel  string `json:"channel"`
	Alias    string `json:"alias"`
	Role     string `json:"role"`
	Inverted bool   `json:"inverted"`
}

type EdgeDoc struct {
	Seq     int    `json:"seq"`
	Channel string `json:"channel"`
	When    string `json:"when"`
	Level   *int   `json:"level"`
}

// Outcome is the import result. Class is one of the model.Class* constants.
type Outcome struct {
	Capture     *model.Capture
	Class       string
	Diagnostics []model.Diagnostic
}

func diag(code, sev, msg string, pos *model.Position) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: sev, Message: msg, Position: pos}
}

// ParseQuantity parses values like "1.0us", "500 ns", "10 MHz", "20ms", "1s",
// "sample:12". Unknown units keep Raw with Valid=false and a diagnostic.
func ParseQuantity(raw, baseUnit string, sampleHz float64, haveRate bool) model.Quantity {
	q := model.Quantity{Raw: raw, BaseUnit: baseUnit}
	tok := strings.TrimSpace(raw)
	if tok == "" {
		q.Valid = false
		q.Diagnosis = ptrDiag(diag("quantity-empty", "error", "empty quantity", nil))
		return q
	}
	if strings.HasPrefix(tok, "sample:") {
		n, err := strconv.Atoi(strings.TrimSpace(tok[len("sample:"):]))
		if err != nil || n < 0 {
			q.Valid = false
			q.Unit = "sample"
			q.Diagnosis = ptrDiag(diag("quantity-bad-sample", "error", "invalid sample index: "+tok, nil))
			return q
		}
		q.Unit = "sample"
		if !haveRate || sampleHz <= 0 {
			q.Valid = false
			q.Diagnosis = ptrDiag(diag("quantity-no-rate", "error", "sample index cannot be normalized without a valid sample rate", nil))
			return q
		}
		q.Valid = true
		q.Value = float64(n) / sampleHz
		return q
	}
	num, unit := splitNumberUnit(tok)
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		q.Valid = false
		q.Unit = unit
		q.Diagnosis = ptrDiag(diag("quantity-bad-number", "error", "cannot parse number in "+raw, nil))
		return q
	}
	mult, ok := unitMultiplier(unit, baseUnit)
	if !ok {
		q.Valid = false
		q.Unit = unit
		q.Diagnosis = ptrDiag(diag("quantity-unknown-unit", "error", "unknown or non-convertible unit "+unit+" in "+raw, nil))
		return q
	}
	q.Valid = true
	q.Unit = unit
	q.Value = v * mult
	return q
}

func ptrDiag(d model.Diagnostic) *model.Diagnostic { return &d }

func splitNumberUnit(tok string) (string, string) {
	i := 0
	for i < len(tok) && (tok[i] == '.' || tok[i] == '-' || tok[i] == '+' || (tok[i] >= '0' && tok[i] <= '9') || tok[i] == 'e' || tok[i] == 'E') {
		i++
	}
	return tok[:i], strings.TrimSpace(tok[i:])
}

// unitMultiplier returns the factor converting value*unit into the base unit.
func unitMultiplier(unit, base string) (float64, bool) {
	u := strings.ToLower(strings.ReplaceAll(unit, " ", ""))
	if base == "s" {
		switch u {
		case "s", "sec", "secs", "second", "seconds":
			return 1, true
		case "ms":
			return 1e-3, true
		case "us", "µs":
			return 1e-6, true
		case "ns":
			return 1e-9, true
		case "ps":
			return 1e-12, true
		}
	}
	if base == "Hz" {
		switch u {
		case "hz":
			return 1, true
		case "khz":
			return 1e3, true
		case "mhz":
			return 1e6, true
		case "ghz":
			return 1e9, true
		}
	}
	return 0, false
}

// Import parses and validates a capture document. It never substitutes a
// default for missing information; problems are returned as located
// diagnostics.
func Import(data []byte, sourceName string) Outcome {
	if len(data) > MaxSourceBytes {
		return Outcome{Class: model.ClassBudget, Diagnostics: []model.Diagnostic{
			diag("source-too-large", "error", fmt.Sprintf("source is %d bytes, budget %d", len(data), MaxSourceBytes), nil),
		}}
	}
	var doc CaptureDoc
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if err := dec.Decode(&doc); err != nil {
		pos := &model.Position{Source: sourceName}
		if se, ok := err.(*json.SyntaxError); ok {
			pos.Line = lineOf(data, se.Offset)
		}
		return Outcome{Class: model.ClassCorrupt, Diagnostics: []model.Diagnostic{
			diag("json-malformed", "error", "malformed capture document: "+err.Error(), pos),
		}}
	}
	var diags []model.Diagnostic
	switch doc.Kind {
	case "i2c", "spi", "sync":
	default:
		diags = append(diags, diag("kind-unknown", "error", "kind must be one of i2c, spi, sync (got "+doc.Kind+")", &model.Position{Source: sourceName}))
	}
	if len(doc.Probes) > MaxProbes {
		return Outcome{Class: model.ClassBudget, Diagnostics: []model.Diagnostic{
			diag("probes-too-many", "error", fmt.Sprintf("probe count %d exceeds budget %d", len(doc.Probes), MaxProbes), &model.Position{Source: sourceName}),
		}}
	}
	if len(doc.Edges) > MaxEdges {
		return Outcome{Class: model.ClassBudget, Diagnostics: []model.Diagnostic{
			diag("edges-too-many", "error", fmt.Sprintf("edge count %d exceeds budget %d", len(doc.Edges), MaxEdges), &model.Position{Source: sourceName}),
		}}
	}

	rate := ParseQuantity(doc.SampleRate, "Hz", 0, false)
	rateHz := 0.0
	haveRate := false
	if rate.Valid && rate.BaseUnit == "Hz" {
		rateHz = rate.Value
		haveRate = true
	} else if strings.TrimSpace(doc.SampleRate) != "" {
		diags = append(diags, withSource(*rate.Diagnosis, sourceName))
	} else {
		diags = append(diags, diag("sample-rate-missing", "error", "sample rate is required (no default is assumed)", &model.Position{Source: sourceName}))
	}

	channelSet := map[string]model.Probe{}
	roles := map[string]bool{}
	for _, p := range doc.Probes {
		if p.Channel == "" {
			diags = append(diags, diag("probe-channel-empty", "error", "probe has empty channel", &model.Position{Source: sourceName}))
			continue
		}
		if _, dup := channelSet[p.Channel]; dup {
			diags = append(diags, diag("probe-channel-duplicate", "error", "duplicate probe channel "+p.Channel, &model.Position{Source: sourceName}))
		}
		if p.Alias == "" {
			diags = append(diags, diag("probe-alias-empty", "error", "probe "+p.Channel+" has no alias (kept verbatim, cannot infer)", &model.Position{Source: sourceName, Channel: p.Channel}))
		}
		if p.Role != "" {
			if roles[p.Role] {
				diags = append(diags, diag("probe-role-duplicate", "warning", "role "+p.Role+" is assigned to multiple probes", &model.Position{Source: sourceName, Channel: p.Channel}))
			}
			roles[p.Role] = true
		}
		channelSet[p.Channel] = model.Probe{Channel: p.Channel, Alias: p.Alias, Role: p.Role, Inverted: p.Inverted}
	}

	capID := model.ShortID("cap", doc.Kind, doc.SourceName, doc.SampleRate, doc.Probes, len(doc.Edges), sourceName)
	edges := make([]model.Edge, 0, len(doc.Edges))
	prevT := -1.0
	for _, e := range doc.Edges {
		pos := &model.Position{Source: sourceName, Channel: e.Channel, EdgeSeq: e.Seq, WhenRaw: e.When}
		if _, ok := channelSet[e.Channel]; !ok {
			diags = append(diags, diag("edge-channel-unknown", "error", "edge references undeclared channel "+e.Channel, pos))
		}
		if e.Level == nil {
			diags = append(diags, diag("edge-level-missing", "error", "edge has no level field", pos))
		} else if *e.Level != 0 && *e.Level != 1 {
			diags = append(diags, diag("edge-level-illegal", "error", fmt.Sprintf("edge level must be 0 or 1, got %d", *e.Level), pos))
		}
		q := ParseQuantity(e.When, "s", rateHz, haveRate)
		if !q.Valid && q.Diagnosis != nil {
			d := *q.Diagnosis
			d.Position = pos
			diags = append(diags, d)
		}
		if q.Valid {
			if q.Value < prevT {
				diags = append(diags, diag("edge-time-order", "warning", "edge time is earlier than a previous edge; ordering uses raw seq", pos))
			}
			prevT = q.Value
		}
		id := model.ShortID("edge", capID, e.Seq, e.Channel, e.When)
		lvl := 0
		if e.Level != nil {
			lvl = *e.Level
		}
		edges = append(edges, model.Edge{ID: id, Channel: e.Channel, When: q, Level: lvl, Seq: e.Seq})
	}

	if len(diags) > 0 && hasError(diags) {
		return Outcome{Class: model.ClassCorrupt, Diagnostics: diags}
	}
	cap := &model.Capture{
		ID:                 capID,
		Kind:               doc.Kind,
		SourceName:         firstNonEmpty(doc.SourceName, sourceName),
		SampleRate:         rate,
		TimeBase:           firstNonEmpty(doc.TimeBase, "s"),
		Probes:             mapToProbes(channelSet),
		Edges:              edges,
		ImportedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		DecoderHintVersion: doc.PackageVersion,
	}
	class := model.ClassOK
	if len(diags) > 0 {
		class = model.ClassOK
	}
	return Outcome{Capture: cap, Class: class, Diagnostics: diags}
}

func withSource(d model.Diagnostic, src string) model.Diagnostic {
	if d.Position == nil {
		d.Position = &model.Position{}
	}
	d.Position.Source = src
	return d
}

func mapToProbes(m map[string]model.Probe) []model.Probe {
	out := make([]model.Probe, 0, len(m))
	for _, p := range m {
		out = append(out, p)
	}
	return out
}

func hasError(ds []model.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func lineOf(data []byte, offset int64) int {
	if offset <= 0 || offset > int64(len(data)) {
		return 0
	}
	line := 1
	for _, b := range data[:offset] {
		if b == '\n' {
			line++
		}
	}
	return line
}

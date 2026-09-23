// Package ingest converts raw digital-channel capture payloads into immutable
// domain.Capture material. It never hides problems: unconvertible quantities
// stay at their original position with a positional Diagnostic, structurally
// broken payloads are reported as FormatCorrupt, and exceeded budgets are
// reported as BudgetExhausted.
package ingest

import (
	"encoding/json"
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

// Payload is the raw import representation. Times and levels stay as original
// tokens; normalization happens here, on the boundary to the derived world.
type Payload struct {
	SchemaVersion string         `json:"schema_version"`
	Capture       CapturePayload `json:"capture"`
}

// CapturePayload is the material exactly as the hardware team exported it.
type CapturePayload struct {
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Bus            string               `json:"bus"`
	TimeBase       string               `json:"time_base"`
	TimeOriginNS   *float64             `json:"time_origin_ns,omitempty"`
	TimeUnit       string               `json:"time_unit"`
	SampleRate     string               `json:"sample_rate"`
	DecoderVersion string               `json:"decoder_version"`
	Channels       []ChannelPayload     `json:"channels"`
	Edges          map[string][]EdgeTok `json:"edges"`
	// RawProps preserves any extra probe properties verbatim.
	RawProps map[string]json.RawMessage `json:"raw_props,omitempty"`
}

// ChannelPayload mirrors a probe channel before normalization.
type ChannelPayload struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`
	Role     string `json:"role"`
	Inverted bool   `json:"inverted"`
	// ClockPolarity is the raw probe clock polarity label (e.g.
	// "active_high"). It is retained on the channel; interpretation polarity
	// for decoding lives in the versioned parameter set.
	ClockPolarity string `json:"clock_polarity,omitempty"`
}

// EdgeTok is the verbatim edge token.
type EdgeTok struct {
	T string `json:"t"`
	L string `json:"l"`
}

// Budget bounds import work so a hostile or damaged capture cannot exhaust the
// process. Zero fields mean "use defaults".
type Budget struct {
	MaxEdges      int
	MaxChannels   int
	MaxEdgeTimeNS float64
}

func (b Budget) withDefaults() Budget {
	if b.MaxEdges == 0 {
		b.MaxEdges = 200000
	}
	if b.MaxChannels == 0 {
		b.MaxChannels = 32
	}
	if b.MaxEdgeTimeNS == 0 {
		b.MaxEdgeTimeNS = 1e12
	}
	return b
}

// Outcome is the explicit three-way result required by the case file.
type Outcome struct {
	Capture     *domain.Capture
	Class       domain.ResultClass
	Diagnostics []domain.Diagnostic
}

// ParsePayload parses JSON without losing unknown fields (they remain in
// RawProps). Structural failure => FormatCorrupt.
func ParsePayload(raw []byte) (*Payload, []domain.Diagnostic) {
	var p Payload
	diag := []domain.Diagnostic{}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, []domain.Diagnostic{{
			Code: "JSON_STRUCTURE", Severity: domain.SeverityFatal,
			Message:  "payload is not well-formed JSON: " + err.Error(),
			Position: domain.Position{JSONPath: "$"},
		}}
	}
	if p.SchemaVersion == "" {
		diag = append(diag, domain.Diagnostic{
			Code: "SCHEMA_VERSION_MISSING", Severity: domain.SeverityError,
			Message:  "schema_version is empty; material retained but decode must not assume a version",
			Position: domain.Position{JSONPath: "$.schema_version"},
		})
	}
	return &p, diag
}

// Build normalizes a parsed payload into immutable domain material.
func Build(p *Payload, budget Budget) Outcome {
	budget = budget.withDefaults()
	diags := []domain.Diagnostic{}
	cp := p.Capture

	if cp.ID == "" {
		return Outcome{Class: domain.ClassFormatCorrupt, Diagnostics: []domain.Diagnostic{{
			Code: "CAPTURE_ID_MISSING", Severity: domain.SeverityFatal,
			Message:  "capture.id is required",
			Position: domain.Position{JSONPath: "$.capture.id"},
		}}}
	}
	switch cp.Bus {
	case "i2c", "spi", "sync":
	default:
		diags = append(diags, domain.Diagnostic{
			Code: "UNKNOWN_BUS", Severity: domain.SeverityFatal,
			Message:  fmt.Sprintf("bus %q is not one of i2c|spi|sync", cp.Bus),
			Position: domain.Position{JSONPath: "$.capture.bus"},
		})
		return Outcome{Class: domain.ClassFormatCorrupt, Diagnostics: diags}
	}

	sr, srOK := domain.ParseSampleRate(cp.SampleRate)
	if !srOK {
		diags = append(diags, domain.Diagnostic{
			Code: "SAMPLE_RATE_UNCONVERTIBLE", Severity: domain.SeverityError,
			Message:  fmt.Sprintf("sample rate %q cannot be normalized; kept verbatim", cp.SampleRate),
			Original: cp.SampleRate, KeptAtPos: true,
			Position: domain.Position{CaptureID: cp.ID, JSONPath: "$.capture.sample_rate"},
		})
	}

	unit := cp.TimeUnit
	if unit == "" {
		unit = "ns"
		diags = append(diags, domain.Diagnostic{
			Code: "TIME_UNIT_DEFAULTED_NOTE", Severity: domain.SeverityWarning,
			Message:  "time_unit absent; displayed as ns, original representation retained per edge",
			Position: domain.Position{CaptureID: cp.ID, JSONPath: "$.capture.time_unit"},
		})
	}

	if len(cp.Channels) > budget.MaxChannels {
		return Outcome{Class: domain.ClassBudgetExhausted, Diagnostics: append(diags, domain.Diagnostic{
			Code: "BUDGET_CHANNELS", Severity: domain.SeverityFatal,
			Message:  fmt.Sprintf("channel count %d exceeds budget %d", len(cp.Channels), budget.MaxChannels),
			Position: domain.Position{CaptureID: cp.ID, JSONPath: "$.capture.channels"},
		})}
	}

	chans := make([]domain.Channel, 0, len(cp.Channels))
	seen := map[string]bool{}
	for i, ch := range cp.Channels {
		if ch.ID == "" {
			diags = append(diags, domain.Diagnostic{
				Code: "CHANNEL_ID_MISSING", Severity: domain.SeverityFatal,
				Message:  fmt.Sprintf("channel at index %d has no id", i),
				Position: domain.Position{CaptureID: cp.ID, JSONPath: fmt.Sprintf("$.capture.channels[%d].id", i)},
			})
			continue
		}
		if seen[ch.ID] {
			diags = append(diags, domain.Diagnostic{
				Code: "CHANNEL_DUPLICATE", Severity: domain.SeverityFatal,
				Message:  fmt.Sprintf("channel %q declared twice", ch.ID),
				Position: domain.Position{CaptureID: cp.ID, JSONPath: fmt.Sprintf("$.capture.channels[%d]", i)},
			})
			continue
		}
		seen[ch.ID] = true
		c := domain.Channel{ID: ch.ID, Alias: ch.Alias, Role: ch.Role, Inverted: ch.Inverted}
		if ch.ClockPolarity != "" {
			c.Role = c.Role + "|clkpol=" + ch.ClockPolarity
		}
		chans = append(chans, c)
	}
	hasFatal := false
	for _, d := range diags {
		if d.Severity == domain.SeverityFatal {
			hasFatal = true
		}
	}
	if hasFatal {
		return Outcome{Class: domain.ClassFormatCorrupt, Diagnostics: diags}
	}

	chByID := map[string]domain.Channel{}
	for _, c := range chans {
		chByID[c.ID] = c
	}

	totalEdges := 0
	for _, es := range cp.Edges {
		totalEdges += len(es)
	}
	if totalEdges > budget.MaxEdges {
		return Outcome{Class: domain.ClassBudgetExhausted, Diagnostics: append(diags, domain.Diagnostic{
			Code: "BUDGET_EDGES", Severity: domain.SeverityFatal,
			Message:  fmt.Sprintf("edge count %d exceeds budget %d", totalEdges, budget.MaxEdges),
			Position: domain.Position{CaptureID: cp.ID, JSONPath: "$.capture.edges"},
		})}
	}

	origin := 0.0
	if cp.TimeOriginNS != nil {
		origin = *cp.TimeOriginNS
	}
	// Source digest is computed from the raw canonical payload before any
	// normalization so identity follows the material as imported.
	digest := domain.HashContent(canonicalRaw(&cp))

	out := &domain.Capture{
		ID: cp.ID, Name: cp.Name, Bus: cp.Bus,
		Probe: domain.ProbeConfig{
			TimeBase: cp.TimeBase, SampleRate: sr, TimeUnit: unit,
			TimeBaseNS: origin, DecoderVersion: cp.DecoderVersion,
		},
		Channels:     chans,
		Edges:        map[string][]domain.Edge{},
		SourceDigest: digest,
		Origin:       "import",
	}

	order := make([]string, 0, len(cp.Edges))
	for k := range cp.Edges {
		order = append(order, k)
	}
	sort.Strings(order)
	for _, chID := range order {
		list := cp.Edges[chID]
		ch, known := chByID[chID]
		if !known {
			diags = append(diags, domain.Diagnostic{
				Code: "EDGE_CHANNEL_UNKNOWN", Severity: domain.SeverityError,
				Message:   fmt.Sprintf("edges reference undeclared channel %q; retained but not normalized", chID),
				KeptAtPos: true,
				Position:  domain.Position{CaptureID: cp.ID, JSONPath: "$.capture.edges." + chID},
			})
			continue
		}
		norm := make([]domain.Edge, 0, len(list))
		lastT := -1.0
		for i, tok := range list {
			pos := domain.Position{CaptureID: cp.ID, ChannelID: chID, EdgeIndex: i,
				JSONPath: fmt.Sprintf("$.capture.edges.%s[%d]", chID, i)}
			q, ok := domain.ParseTime(tok.T, unit)
			if !ok {
				pos.EdgeID = ""
				diags = append(diags, domain.Diagnostic{
					Code: "EDGE_TIME_UNCONVERTIBLE", Severity: domain.SeverityError,
					Message:  fmt.Sprintf("edge time %q cannot be converted to ns; value kept at position, edge omitted from normalized stream", tok.T),
					Original: tok.T, KeptAtPos: true, Position: pos,
				})
				continue
			}
			if q.Value > budget.MaxEdgeTimeNS {
				return Outcome{Class: domain.ClassBudgetExhausted, Diagnostics: append(diags, domain.Diagnostic{
					Code: "BUDGET_TIME_WINDOW", Severity: domain.SeverityFatal,
					Message:  fmt.Sprintf("edge time %.0fns exceeds budget window %.0fns", q.Value, budget.MaxEdgeTimeNS),
					Position: pos,
				})}
			}
			lvl, ltok, lok := domain.ParseLevel(tok.L, ch.Inverted)
			if !lok {
				diags = append(diags, domain.Diagnostic{
					Code: "EDGE_LEVEL_UNKNOWN", Severity: domain.SeverityError,
					Message:  fmt.Sprintf("edge level %q is not 0/1/low/high; kept verbatim, edge omitted from normalized stream", tok.L),
					Original: tok.L, KeptAtPos: true, Position: pos,
				})
				continue
			}
			if len(norm) > 0 && q.Value < lastT {
				diags = append(diags, domain.Diagnostic{
					Code: "EDGE_OUT_OF_ORDER", Severity: domain.SeverityError,
					Message:  fmt.Sprintf("edge time %s precedes previous edge; stream is treated as corrupt from here", tok.T),
					Original: tok.T, KeptAtPos: true, Position: pos,
				})
				return Outcome{Class: domain.ClassFormatCorrupt, Capture: out, Diagnostics: append(diags, domain.Diagnostic{})}
			}
			lastT = q.Value
			tRel := q.Value + origin
			id := domain.EdgeIdentity(digest, chID, i, tRel, lvl)
			pos.EdgeID = id
			pos.TimeNS = tRel
			norm = append(norm, domain.Edge{
				ID: id, Index: i, TimeNS: tRel, Level: lvl,
				OrigTok: tok.T, OrigLvl: ltok,
			})
		}
		out.Edges[chID] = norm
	}

	out.Diagnostics = diags
	return Outcome{Capture: out, Class: domain.ClassOK, Diagnostics: diags}
}

// canonicalRaw digests the payload including original tokens and channel
// metadata, but excludes nothing: identity must move with the raw material.
func canonicalRaw(cp *CapturePayload) map[string]any {
	edges := map[string]any{}
	for k, v := range cp.Edges {
		lst := make([]any, len(v))
		for i, e := range v {
			lst[i] = map[string]any{"t": e.T, "l": e.L}
		}
		edges[k] = lst
	}
	chans := make([]any, len(cp.Channels))
	for i, c := range cp.Channels {
		chans[i] = map[string]any{
			"id": c.ID, "alias": c.Alias, "role": c.Role,
			"inverted": c.Inverted, "clock_polarity": c.ClockPolarity,
		}
	}
	return map[string]any{
		"id": cp.ID, "bus": cp.Bus, "time_base": cp.TimeBase,
		"time_origin_ns": cp.TimeOriginNS, "time_unit": cp.TimeUnit,
		"sample_rate": cp.SampleRate, "decoder_version": cp.DecoderVersion,
		"channels": chans, "edges": edges, "raw_props": cp.RawProps,
	}
}

// Note stores an unconvertible quantity verbatim inside RawProps so it is
// never lost "for the sake of drawing a picture".
func KeepRawProp(p *Payload, key string, raw json.RawMessage) {
	if p.Capture.RawProps == nil {
		p.Capture.RawProps = map[string]json.RawMessage{}
	}
	p.Capture.RawProps[key] = raw
}

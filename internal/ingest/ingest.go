// Package ingest turns the hardware team's JSON export of digital channel
// edges and probe configuration into immutable model.Capture material.
//
// The three failure classes mandated by the domain are distinct types so the
// storage and HTTP layers can report them differently:
//
//   - FormatError  : the bytes themselves are structurally corrupt.
//   - SemanticError: structurally valid but the domain has no interpretation.
//   - BudgetError  : the resource budget was exhausted while reading.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"edgebench/internal/model"
	"edgebench/internal/qty"
)

// Budget caps input resources.
type Budget struct {
	MaxBytes int
	MaxEdges int
}

// DefaultBudget used by the HTTP path.
var DefaultBudget = Budget{MaxBytes: 16 << 20, MaxEdges: 200_000}

// Position points at an input location, e.g. "edges[12].time".
type Position string

// FormatError means malformed JSON / unknown field / wrong scalar type.
type FormatError struct {
	Pos Position
	Msg string
}

func (e *FormatError) Error() string {
	if e.Pos != "" {
		return fmt.Sprintf("format corrupt at %s: %s", e.Pos, e.Msg)
	}
	return "format corrupt: " + e.Msg
}

// SemanticError means well-formed input with no legal interpretation.
type SemanticError struct {
	Pos Position
	Msg string
}

func (e *SemanticError) Error() string {
	if e.Pos != "" {
		return fmt.Sprintf("semantically unresolvable at %s: %s", e.Pos, e.Msg)
	}
	return "semantically unresolvable: " + e.Msg
}

// BudgetError means a configured resource limit was reached.
type BudgetError struct {
	Limit string
	Msg   string
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("budget exhausted (%s): %s", e.Limit, e.Msg)
}

// --- wire format -----------------------------------------------------------

type wireChannel struct {
	Index  int    `json:"index"`
	Alias  string `json:"alias"`
	Invert bool   `json:"invert"`
}

type wireEdge struct {
	Channel string `json:"channel"`
	Time    string `json:"time"`
	Level   *int   `json:"level"`
}

type wireBus struct {
	ID             string            `json:"id"`
	Kind           string            `json:"kind"`
	SCL            string            `json:"scl"`
	SDA            string            `json:"sda"`
	MOSI           string            `json:"mosi"`
	MISO           string            `json:"miso"`
	Clock          string            `json:"clock"`
	Data           string            `json:"data"`
	CS             map[string]string `json:"cs"`
	ClockPolarity  string            `json:"clock_polarity"`
}

type wireCapture struct {
	Name       string        `json:"name"`
	SampleRate string        `json:"sample_rate"`
	Channels   []wireChannel `json:"channels"`
	Edges      []wireEdge    `json:"edges"`
	Buses      []wireBus     `json:"buses"`
}

// Parse validates and normalizes one capture document.
func Parse(data []byte, b Budget) (*model.Capture, error) {
	if len(data) > b.MaxBytes {
		return nil, &BudgetError{Limit: "bytes", Msg: fmt.Sprintf("input %d > %d", len(data), b.MaxBytes)}
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var w wireCapture
	if err := dec.Decode(&w); err != nil {
		return nil, &FormatError{Msg: "invalid JSON: " + err.Error()}
	}
	if dec.More() {
		return nil, &FormatError{Msg: "multiple JSON documents in one import"}
	}

	if w.Name == "" {
		return nil, &SemanticError{Pos: "name", Msg: "capture name is required"}
	}

	sampleRate := qty.ParseRate(w.SampleRate)
	if w.SampleRate == "" {
		return nil, &SemanticError{Pos: "sample_rate", Msg: "sample rate is required (no default is invented)"}
	}
	if !sampleRate.OK {
		return nil, &SemanticError{Pos: "sample_rate", Msg: sampleRate.Reason}
	}

	if len(w.Channels) == 0 {
		return nil, &SemanticError{Pos: "channels", Msg: "at least one channel required"}
	}
	alias := map[string]bool{}
	seenIndex := map[int]bool{}
	chans := make([]model.Channel, 0, len(w.Channels))
	for i, c := range w.Channels {
		pos := Position(fmt.Sprintf("channels[%d]", i))
		if c.Alias == "" {
			return nil, &SemanticError{Pos: pos + ".alias", Msg: "channel alias must be preserved; empty alias not allowed"}
		}
		if alias[c.Alias] {
			return nil, &SemanticError{Pos: pos + ".alias", Msg: "duplicate channel alias: " + c.Alias}
		}
		if seenIndex[c.Index] {
			return nil, &SemanticError{Pos: pos + ".index", Msg: "duplicate channel index"}
		}
		seenIndex[c.Index] = true
		alias[c.Alias] = true
		chans = append(chans, model.Channel{Index: c.Index, Alias: c.Alias, Invert: c.Invert})
	}

	if len(w.Edges) > b.MaxEdges {
		return nil, &BudgetError{Limit: "edges", Msg: fmt.Sprintf("%d > %d", len(w.Edges), b.MaxEdges)}
	}

	// Stable channel ordering (by physical index) and stable per-channel edge
	// sequence numbers (by normalized time; raw representation is retained).
	perChan := map[string][]model.Edge{}
	var diags []model.Diagnostic
	unconvertible := 0
	for i, e := range w.Edges {
		pos := Position(fmt.Sprintf("edges[%d]", i))
		if !alias[e.Channel] {
			return nil, &SemanticError{Pos: pos + ".channel", Msg: "unknown channel alias: " + e.Channel}
		}
		if e.Level == nil || (*e.Level != 0 && *e.Level != 1) {
			return nil, &SemanticError{Pos: pos + ".level", Msg: "level must be 0 or 1"}
		}
		if e.Time == "" {
			return nil, &SemanticError{Pos: pos + ".time", Msg: "edge time is required (no default zero is invented)"}
		}
		tq := qty.ParseTime(e.Time)
		if !tq.OK {
			// Unconvertible values stay in place with a diagnostic instead of
			// being dropped for rendering.
			unconvertible++
			diags = append(diags, model.Diagnostic{
				Code: "EDGE_TIME_UNCONVERTIBLE", Severity: model.SevWarning,
				Message: tq.Reason, Pos: string(pos) + ".time",
			})
		}
		inverted := *e.Level
		if invChan(chans, e.Channel) {
			inverted ^= 1
		}
		perChan[e.Channel] = append(perChan[e.Channel], model.Edge{
			Channel: e.Channel, Time: tq, Level: inverted, RawLevel: *e.Level,
		})
	}

	buses, err := buildBuses(w.Buses, alias)
	if err != nil {
		return nil, err
	}

	// Assign stable per-channel sequence numbers and stable edge identities.
	all := make([]model.Edge, 0, len(w.Edges))
	for _, c := range chans {
		list := sortEdgesStable(perChan[c.Alias])
		for i := range list {
			list[i].Seq = i
			list[i].EdgeID = edgeIdentity(c.Alias, i, list[i])
		}
		all = append(all, list...)
	}

	cap := &model.Capture{
		Name:        w.Name,
		SampleRate:  sampleRate,
		Channels:    chans,
		Edges:       all,
		Buses:       buses,
		Diagnostics: diags,
	}
	cap.CaptureID = model.CaptureDigest(cap)

	// If too many edges lack a coordinate, the semantics are unresolvable:
	// nothing is rendered from invented positions.
	if unconvertible > 0 && float64(unconvertible) > 0.02*float64(len(w.Edges)) {
		return nil, &SemanticError{Pos: "edges.time", Msg: fmt.Sprintf("%d edge times cannot be converted to the time basis", unconvertible)}
	}
	return cap, nil
}

func invChan(chans []model.Channel, alias string) bool {
	for _, c := range chans {
		if c.Alias == alias {
			return c.Invert
		}
	}
	return false
}

func buildBuses(in []wireBus, alias map[string]bool) ([]model.Bus, error) {
	out := make([]model.Bus, 0, len(in))
	for i, b := range in {
		pos := Position(fmt.Sprintf("buses[%d]", i))
		kind := model.BusKind(b.Kind)
		if kind != model.BusI2C && kind != model.BusSPI && kind != model.BusSync {
			return nil, &SemanticError{Pos: pos + ".kind", Msg: "kind must be i2c, spi or sync"}
		}
		if b.ID == "" {
			return nil, &SemanticError{Pos: pos + ".id", Msg: "bus id required"}
		}
		if b.ClockPolarity != "" && b.ClockPolarity != "normal" && b.ClockPolarity != "inverted" {
			return nil, &SemanticError{Pos: pos + ".clock_polarity", Msg: `clock polarity must be "normal" or "inverted"`}
		}
		check := func(field, a string) error {
			if a != "" && !alias[a] {
				return &SemanticError{Pos: pos + Position("."+field), Msg: "unknown channel alias: " + a}
			}
			return nil
		}
		switch kind {
		case model.BusI2C:
			if b.SCL == "" || b.SDA == "" {
				return nil, &SemanticError{Pos: pos, Msg: "i2c bus requires scl and sda aliases"}
			}
		case model.BusSPI:
			if b.MOSI == "" || len(b.CS) == 0 {
				return nil, &SemanticError{Pos: pos, Msg: "spi bus requires mosi and at least one cs"}
			}
		case model.BusSync:
			if b.Clock == "" || b.Data == "" {
				return nil, &SemanticError{Pos: pos, Msg: "sync bus requires clock and data aliases"}
			}
		}
		for _, pair := range [][2]string{{"scl", b.SCL}, {"sda", b.SDA}, {"mosi", b.MOSI}, {"miso", b.MISO}, {"clock", b.Clock}, {"data", b.Data}} {
			if err := check(pair[0], pair[1]); err != nil {
				return nil, err
			}
		}
		for a := range b.CS {
			if !alias[a] {
				return nil, &SemanticError{Pos: pos + ".cs", Msg: "unknown channel alias: " + a}
			}
		}
		out = append(out, model.Bus{
			BusID: b.ID, Kind: kind, SCL: b.SCL, SDA: b.SDA, MOSI: b.MOSI, MISO: b.MISO,
			Clock: b.Clock, Data: b.Data, CS: b.CS, ClockPolarity: b.ClockPolarity,
		})
	}
	return out, nil
}

// Package decode turns immutable edge captures into reviewable bus
// transactions. Decoders never mutate the material: every byte carries the
// identities of the edges it was derived from, every ambiguity becomes a
// retained candidate, and every anomaly becomes a structured diagnostic with
// a position (channel, edge, time).
package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// DecoderVersion is stamped on every run so historical conclusions keep the
// rule version they were computed with.
const DecoderVersion = "edge-decode/1.4.0"

// Byte is one decoded byte (or partial byte) with full edge provenance.
type Byte struct {
	ID        string   `json:"id"`
	Value     int      `json:"value"`
	Bits      int      `json:"bits"` // number of data bits actually seen
	Ack       string   `json:"ack"`  // "" | "ack" | "nack" (i2c 9th bit)
	EdgeIDs   []string `json:"edge_ids"`
	StartNS   int64    `json:"start_ns"`
	EndNS     int64    `json:"end_ns"`
	Channel   string   `json:"channel"` // data channel alias
	Direction string   `json:"direction,omitempty"`
}

// Transaction is one bus transaction. A data gap always closes a
// transaction; bytes are never stitched across a gap.
type Transaction struct {
	ID        string   `json:"id"`
	Bus       string   `json:"bus"` // i2c | spi | sync
	Device    string   `json:"device,omitempty"`
	Bytes     []Byte   `json:"bytes"`
	StartNS   int64    `json:"start_ns"`
	EndNS     int64    `json:"end_ns"`
	Flags     []string `json:"flags,omitempty"` // repeated_start | clock_stretch | gap_terminated | partial
	Channel   string   `json:"channel,omitempty"`
	Direction string   `json:"direction,omitempty"`
}

// Candidate is one legal interpretation retained when the input does not
// determine a unique decode. Candidates are never silently dropped.
type Candidate struct {
	ID           string             `json:"id"`
	Key          string             `json:"key"` // deterministic sort key
	Label        string             `json:"label"`
	Reason       string             `json:"reason"` // why this candidate exists
	Confidence   float64            `json:"confidence"`
	Status       string             `json:"status"` // pending | adopted | rejected
	Transactions []Transaction      `json:"transactions"`
	Diagnostics  []model.Diagnostic `json:"diagnostics"`
}

// Result is the per-bus outcome of one decode run.
type Result struct {
	Kind        string             `json:"kind"` // ok | budget_exhausted | corrupt | unsolvable
	Bus         string             `json:"bus"`
	Candidates  []Candidate        `json:"candidates"`
	Diagnostics []model.Diagnostic `json:"diagnostics"`
}

// Run is the full decode output for one (material, params) pair.
type Run struct {
	ID           string            `json:"id"`
	MaterialID   string            `json:"material_id"`
	ParamsID     string            `json:"params_id"`
	ParamsVer    int               `json:"params_version"`
	DecoderVer   string            `json:"decoder_version"`
	InputDigest  string            `json:"input_digest"`
	Kind         string            `json:"kind"`
	Results      map[string]Result `json:"results"` // by bus
	Diagnostics  []model.Diagnostic `json:"diagnostics"`
}

// levelTimeline reconstructs logical levels from edges of one channel.
type levelTimeline struct {
	channel model.Channel
	edges   []model.Edge // time-sorted, valid only
}

func newTimeline(m *model.Material, ch model.Channel) *levelTimeline {
	edges := m.EdgesFor(ch.ID)
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].TimeNS < edges[j].TimeNS })
	return &levelTimeline{channel: ch, edges: edges}
}

// levelAt returns the logical level at time t (inclusive of an edge exactly
// at t), applying the channel inversion flag.
func (lt *levelTimeline) levelAt(t int64) int {
	lvl := lt.channel.Initial
	for _, e := range lt.edges {
		if e.TimeNS > t {
			break
		}
		lvl = e.Level
	}
	if lt.channel.Inverted {
		return 1 - lvl
	}
	return lvl
}

// levelJustBefore returns the logical level immediately before time t.
func (lt *levelTimeline) levelJustBefore(t int64) int {
	lvl := lt.channel.Initial
	for _, e := range lt.edges {
		if e.TimeNS >= t {
			break
		}
		lvl = e.Level
	}
	if lt.channel.Inverted {
		return 1 - lvl
	}
	return lvl
}

// clockEdge is one sampling-relevant clock transition.
type clockEdge struct {
	edge   model.Edge
	rising bool // logical direction after inversion
}

// clockEdges returns the clock transitions of the given polarity. The clock
// polarity parameter selects which logical edge samples data; inversion flags
// on the channel are applied first, so the parameter always speaks in
// logical terms.
func clockEdges(m *model.Material, ch model.Channel, polarity string) []clockEdge {
	var out []clockEdge
	for _, e := range m.EdgesFor(ch.ID) {
		if !e.TimeOK {
			continue
		}
		logical := e.Level
		if ch.Inverted {
			logical = 1 - logical
		}
		rising := logical == 1
		if (polarity == "rising" && rising) || (polarity == "falling" && !rising) {
			out = append(out, clockEdge{edge: e, rising: rising})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].edge.TimeNS < out[j].edge.TimeNS })
	return out
}

// validateMaterial checks structural integrity of the capture. Format
// corruption is reported as its own result kind, distinct from budget
// exhaustion and semantic unsolvability.
func validateMaterial(m *model.Material, roles ...string) []model.Diagnostic {
	var diags []model.Diagnostic
	known := map[string]bool{}
	for _, c := range m.Channels {
		known[c.ID] = true
	}
	lastByCh := map[string]int64{}
	for _, e := range m.Edges {
		if !known[e.Channel] {
			diags = append(diags, model.Diagnostic{
				Code: model.DiagUnknownChannel, Severity: "error", EdgeID: e.ID,
				Message: fmt.Sprintf("edge %s references unknown channel %q", e.ID, e.Channel),
			})
			continue
		}
		if !e.TimeOK {
			diags = append(diags, model.Diagnostic{
				Code: model.DiagUnconvertibleTime, Severity: "error", Channel: e.Channel, EdgeID: e.ID,
				Message: fmt.Sprintf("edge %s raw time %q cannot be converted to ns; kept in place", e.ID, e.RawTime),
			})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, model.Diagnostic{
				Code: model.DiagBadLevel, Severity: "error", Channel: e.Channel, EdgeID: e.ID, TimeNS: e.TimeNS,
				Message: fmt.Sprintf("edge %s has invalid level %d", e.ID, e.Level),
			})
		}
		if last, ok := lastByCh[e.Channel]; ok && e.TimeNS < last {
			diags = append(diags, model.Diagnostic{
				Code: model.DiagNonMonotonic, Severity: "error", Channel: e.Channel, EdgeID: e.ID, TimeNS: e.TimeNS,
				Message: fmt.Sprintf("edge %s time %d goes backwards (previous %d)", e.ID, e.TimeNS, last),
			})
		}
		lastByCh[e.Channel] = e.TimeNS
	}
	return diags
}

// hasErrors reports whether any diagnostic is an error.
func hasErrors(diags []model.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

// sortCandidates orders candidates deterministically: confidence desc, then
// key asc. Equivalent inputs therefore always produce a stable order.
func sortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].Key < cs[j].Key
	})
}

// assignIDs gives every candidate, transaction and byte a stable identity
// derived from the run and its position, so exports and re-imports keep the
// same IDs.
func assignIDs(runID string, c *Candidate) {
	c.ID = model.ShortHash(runID, c.Key)
	for ti := range c.Transactions {
		tx := &c.Transactions[ti]
		tx.ID = model.ShortHash(c.ID, "tx", ti)
		for bi := range tx.Bytes {
			b := &tx.Bytes[bi]
			b.ID = model.ShortHash(tx.ID, "byte", bi)
		}
	}
}

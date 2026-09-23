// Package model defines the domain types for the edge-decoding workbench.
// Layering rule: captures (raw edges, sample rate, aliases, inversion) are
// immutable material; params and decisions are versioned, mutable-by-append
// layers; runs/candidates/transactions are derived and record their inputs.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// Decode status values. Resource-budget exhaustion, corrupt formats and
// semantically unsolvable input are three distinct results, never conflated.
const (
	StatusOK                 = "ok"
	StatusBudgetExhausted    = "budget_exhausted"
	StatusFormatCorrupt      = "format_corrupt"
	StatusSemanticUnsolvable = "semantic_unsolvable"
)

// Diagnostic categories.
const (
	DiagInvalidValue  = "invalid_value"
	DiagFormatCorrupt = "format_corrupt"
	DiagBudget        = "budget_exhausted"
	DiagSemantic      = "semantic_unsolvable"
)

// Event kinds (machine-readable judgement records).
const (
	EvImport           = "import"
	EvDecodeRun        = "decode_run"
	EvRepeatedStart    = "repeated_start"
	EvClockStretch     = "clock_stretch"
	EvClockEdgeMissing = "clock_edge_missing"
	EvDataGapNoStitch  = "data_gap_no_stitch"
	EvBoundarySample   = "boundary_sample"
	EvMultiCandidate   = "multi_candidate"
	EvMultiCS          = "multi_cs"
	EvSPIModeUnknown   = "spi_mode_unknown"
	EvParamRevision    = "param_revision"
	EvSourceChange     = "source_change"
	EvDecision         = "decision"
	EvDecisionConflict = "decision_conflict"
	EvCandidateKept    = "candidate_kept"
	EvExport           = "export"
)

// Edge is one immutable digital transition. TimeNs is the canonical derived
// value; Raw keeps the original representation exactly as imported.
type Edge struct {
	ID      string `json:"id"`
	Channel string `json:"channel"` // channel alias
	TimeNs  int64  `json:"time_ns"` // canonical time base: nanoseconds
	Level   int    `json:"level"`   // 0 or 1, as probed (inversion NOT applied)
	Raw     string `json:"raw"`     // original textual representation
}

// Channel describes a probe alias and its electrical interpretation.
type Channel struct {
	Alias    string `json:"alias"`
	Role     string `json:"role"` // scl,sda,sclk,mosi,miso,cs,clk,data,en
	Inverted bool   `json:"inverted"`
}

// Capture is an immutable bundle of material. A localized source change
// produces a new Capture whose ParentID points at its ancestor.
type Capture struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	SampleRateHz int64        `json:"sample_rate_hz"`
	Channels     []Channel    `json:"channels"`
	Edges        []Edge       `json:"edges"`
	ParentID     string       `json:"parent_id,omitempty"`
	Note         string       `json:"note,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
}

// Params is one version of decoder/probe configuration.
type Params struct {
	ID                  string `json:"id"`
	ParentID            string `json:"parent_id,omitempty"`
	Label               string `json:"label"`
	ClockPolarity       string `json:"clock_polarity"` // rising|falling (sync bus + SPI CPHA base)
	SPIMode             string `json:"spi_mode"`       // unknown|0|1|2|3
	GapThresholdNs      int64  `json:"gap_threshold_ns"`
	StretchThresholdNs  int64  `json:"stretch_threshold_ns"`
	DecoderVersion      string `json:"decoder_version"`
	BudgetMaxEdges      int    `json:"budget_max_edges"`
	BudgetMaxCandidates int    `json:"budget_max_candidates"`
}

// Diagnostic is always located; failures never masquerade as empty success.
type Diagnostic struct {
	Category string `json:"category"`
	Channel  string `json:"channel,omitempty"`
	TimeNs   int64  `json:"time_ns,omitempty"`
	Position string `json:"position"` // human/machine locatable position
	Message  string `json:"message"`
}

// Byte is one decoded byte. EdgeIDs link every byte back to its source edges.
type Byte struct {
	ID        string   `json:"id"`
	Index     int      `json:"index"`
	Value     int      `json:"value"` // 0..255
	Ack       string   `json:"ack"`   // ACK|NACK|"" (empty on buses without ack)
	StartNs   int64    `json:"start_ns"`
	EndNs     int64    `json:"end_ns"`
	EdgeIDs   []string `json:"edge_ids"`
	Ambiguous bool     `json:"ambiguous,omitempty"`
}

// Transaction is one bus transaction; a data gap never stitches two of them.
type Transaction struct {
	ID      string   `json:"id"`
	Bus     string   `json:"bus"` // i2c|spi|sync
	Device  string   `json:"device,omitempty"`
	Address int      `json:"address,omitempty"`
	Read    bool     `json:"read,omitempty"`
	Bytes   []Byte   `json:"bytes"`
	StartNs int64    `json:"start_ns"`
	EndNs   int64    `json:"end_ns"`
	Flags   []string `json:"flags,omitempty"`
}

// Candidate is one legal decode hypothesis. Ambiguity keeps several.
type Candidate struct {
	ID           string        `json:"id"`
	Bus          string        `json:"bus"`
	Reason       string        `json:"reason"` // why this candidate exists
	Confidence   float64       `json:"confidence"`
	Transactions []Transaction `json:"transactions"`
}

// Run is a derived decode result recording all of its inputs.
type Run struct {
	ID             string       `json:"id"`
	CaptureID      string       `json:"capture_id"`
	ParamsID       string       `json:"params_id"`
	DecoderVersion string       `json:"decoder_version"`
	InputDigest    string       `json:"input_digest"`
	Status         string       `json:"status"`
	Candidates     []Candidate  `json:"candidates"`
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
}

// Event is a machine-readable judgement with full dependency provenance.
type Event struct {
	ID          string            `json:"id"`
	Seq         int64             `json:"seq"`
	Kind        string            `json:"kind"`
	SubjectType string            `json:"subject_type"`
	SubjectID   string            `json:"subject_id"`
	Rationale   string            `json:"rationale"`
	Deps        map[string]string `json:"deps"` // capture/params/run/decoder_version
}

// Decision is a human accept/reject/adjust. Conflicts are kept, not merged.
type Decision struct {
	ID          string `json:"id"`
	Seq         int64  `json:"seq"`
	Reviewer    string `json:"reviewer"`
	Action      string `json:"action"` // accept|reject|adjust
	CandidateID string `json:"candidate_id"`
	Note        string `json:"note"`
}

// HashID derives a stable, content-addressed identity.
func HashID(prefix string, parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(h.Sum(nil))[:16])
}

// CanonicalJSON renders deterministic JSON (sorted keys, stable field order).
func CanonicalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// SortEdges orders edges deterministically: equivalent inputs in any
// permutation yield the same canonical order.
func SortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.TimeNs != b.TimeNs {
			return a.TimeNs < b.TimeNs
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Level < b.Level
	})
}

// AssignIDs recomputes stable identities for the whole capture.
func (c *Capture) AssignIDs() {
	SortEdges(c.Edges)
	seen := map[string]int{}
	for i := range c.Edges {
		e := &c.Edges[i]
		key := fmt.Sprintf("%s|%d|%d", e.Channel, e.TimeNs, e.Level)
		seen[key]++
		e.ID = HashID("edg", e.Channel, e.TimeNs, e.Level, seen[key])
	}
}

// ComputeCaptureID derives identity from material content only.
func ComputeCaptureID(name string, sampleRate int64, chans []Channel, edges []Edge, parentID string) string {
	type minimalEdge struct {
		Channel string `json:"c"`
		TimeNs  int64  `json:"t"`
		Level   int    `json:"l"`
	}
	sorted := append([]Edge(nil), edges...)
	SortEdges(sorted)
	me := make([]minimalEdge, len(sorted))
	for i, e := range sorted {
		me[i] = minimalEdge{e.Channel, e.TimeNs, e.Level}
	}
	return HashID("cap", name, sampleRate, chans, me, parentID)
}

// ComputeParamsID derives identity from parameter content + lineage.
func ComputeParamsID(p Params) string {
	return HashID("par", p.ParentID, p.Label, p.ClockPolarity, p.SPIMode,
		p.GapThresholdNs, p.StretchThresholdNs, p.DecoderVersion,
		p.BudgetMaxEdges, p.BudgetMaxCandidates)
}

// ComputeRunID derives identity from the full dependency triple.
func ComputeRunID(captureID, paramsID, decoderVersion string) string {
	return HashID("run", captureID, paramsID, decoderVersion)
}

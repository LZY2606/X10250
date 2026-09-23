// Package model defines the domain types for the edge-decoding workbench.
// Layers: immutable materials (raw imports), versioned parameter sets,
// derived decode runs with candidates, and mutable decisions recorded as
// events. Raw representations are always preserved; normalization happens
// only in the derived layer.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Diagnostic codes. The three hard failure classes must stay distinct:
// resource budget exhausted, format corrupt, semantically unsolvable.
const (
	DiagResourceExhausted   = "resource_exhausted"
	DiagFormatCorrupt       = "format_corrupt"
	DiagSemanticUnsolvable  = "semantic_unsolvable"
	DiagUnitUnconvertible   = "unit_unconvertible"
	DiagAmbiguousClock      = "ambiguous_clock"
	DiagMissingClockEdge    = "missing_clock_edge"
	DiagClockStretch        = "clock_stretch"
	DiagDataGap             = "data_gap"
	DiagBoundarySample      = "boundary_sample"
	DiagRepeatedStart       = "repeated_start"
	DiagSPIModeUnknown      = "spi_mode_unknown"
	DiagLevelUnresolvable   = "level_unresolvable"
)

type Diagnostic struct {
	Code     string `json:"code"`
	Position string `json:"position"` // e.g. "channels[1].edges[42]" or "edge#17"
	Message  string `json:"message"`
}

// Quantity keeps the raw representation plus its unit/basis. Derived layers
// normalize to nanoseconds; unconvertible values keep Raw and get a
// diagnostic instead of a fabricated default.
type Quantity struct {
	Raw   string  `json:"raw"`
	Value float64 `json:"value"` // normalized to Unit
	Unit  string  `json:"unit"`  // "ns","us","ms","s","samples","Hz"
	Basis string  `json:"basis"` // coordinate basis, e.g. "session_start"
}

type Channel struct {
	Name     string `json:"name"`  // physical probe, e.g. "D0"
	Alias    string `json:"alias"` // logical name, e.g. "SCL"
	Inverted bool   `json:"inverted"`
	Role     string `json:"role"` // scl,sda,sclk,mosi,miso,cs,clock,data
}

type Edge struct {
	ID        int64   `json:"id"`        // stable identity: ordinal within material
	Channel   string  `json:"channel"`   // physical channel name
	TimeRaw   string  `json:"time_raw"`  // original representation, preserved
	Time      float64 `json:"time"`      // normalized ns (0 when unconvertible)
	Unit      string  `json:"unit"`      // unit of TimeRaw as imported
	Basis     string  `json:"basis"`     // time basis
	Direction int     `json:"direction"` // +1 rising, -1 falling, as recorded by probe
	Valid     bool    `json:"valid"`     // false when time could not be normalized
}

// Material is an immutable imported capture: edges + probe configuration.
type Material struct {
	ID             string    `json:"id"` // content hash
	Name           string    `json:"name"`
	SampleRateHz   Quantity  `json:"sample_rate"`
	ClockPolarity  string    `json:"clock_polarity"` // "idle_low" | "idle_high"
	DecoderVersion string    `json:"decoder_version"`
	Channels       []Channel `json:"channels"`
	Edges          []Edge    `json:"edges"`
	EventSeq       int64     `json:"event_seq"`
}

type Params struct {
	Protocol        string   `json:"protocol"` // i2c | spi | sync
	ClockPolarity   string   `json:"clock_polarity"`
	SPIMode         int      `json:"spi_mode"` // -1 = unknown -> keep all 4 candidates
	SampleEdge      string   `json:"sample_edge"`
	GapThresholdNs  float64  `json:"gap_threshold_ns"`
	StretchThreshNs float64  `json:"stretch_threshold_ns"`
	BoundaryEpsNs   float64  `json:"boundary_eps_ns"`
	MaxCandidates   int      `json:"max_candidates"` // resource budget
	CSChannels      []string `json:"cs_channels"`
}

// ParamSet is an immutable, versioned parameter snapshot.
type ParamSet struct {
	ID       string `json:"id"`
	Version  int    `json:"version"`
	ParentID string `json:"parent_id"`
	Note     string `json:"note"`
	Params   Params `json:"params"`
	EventSeq int64  `json:"event_seq"`
}

// Run is a derived decode result with full provenance.
type Run struct {
	ID          string `json:"id"`
	MaterialID  string `json:"material_id"`  // input digest
	ParamSetID  string `json:"param_set_id"` // parameter version
	ParentRunID string `json:"parent_run_id"`
	Status      string `json:"status"` // ok | resource_exhausted | format_corrupt | semantic_unsolvable
	EventSeq    int64  `json:"event_seq"`
}

type Candidate struct {
	ID         string  `json:"id"`
	RunID      string  `json:"run_id"`
	Kind       string  `json:"kind"` // i2c | spi | sync
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	Ordinal    int     `json:"ordinal"` // stable order within run
}

type Transaction struct {
	ID           string  `json:"id"`
	CandidateID  string  `json:"candidate_id"`
	Ordinal      int     `json:"ordinal"`
	GroupID      string  `json:"group_id"` // repeated-START chains share a group
	Device       string  `json:"device"`   // CS alias for SPI
	TerminatedBy string  `json:"terminated_by"` // stop | cs_rise | gap | eob | repeated_start
	StartNs      float64 `json:"start_ns"`
	EndNs        float64 `json:"end_ns"`
}

// Frame is one decoded byte, traceable back to edges.
type Frame struct {
	ID          string  `json:"id"`
	CandidateID string  `json:"candidate_id"`
	TxID        string  `json:"tx_id"`
	Ordinal     int     `json:"ordinal"`
	Value       int     `json:"value"`
	Ack         string  `json:"ack"` // "" | "ACK" | "NACK"
	Line        string  `json:"line"` // mosi | miso | data
	StartNs     float64 `json:"start_ns"`
	EndNs       float64 `json:"end_ns"`
	EdgeRefs    []int64 `json:"edge_refs"` // material edge IDs
}

type Decision struct {
	ID       string `json:"id"`
	EventSeq int64  `json:"event_seq"`
	TargetID string `json:"target_id"` // candidate ID
	Action   string `json:"action"`    // accept | reject
	Actor    string `json:"actor"`
	Branch   string `json:"branch"`
	Note     string `json:"note"`
}

// Event is the machine-readable journal entry. Every import, decode,
// decision, amendment, export and import appends exactly one event.
type Event struct {
	Seq     int64    `json:"seq"`
	Type    string   `json:"type"`
	Summary  string   `json:"summary"`
	Deps    []string `json:"deps"` // parent dependency IDs
	Payload string   `json:"payload"` // JSON
}

// Hash returns the content-addressed identity of a JSON-marshalable value.
func Hash(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// SortCandidatesStable gives equivalent inputs a stable order:
// confidence descending, then ordinal, then ID.
func SortCandidatesStable(cs []*Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		if cs[i].Ordinal != cs[j].Ordinal {
			return cs[i].Ordinal < cs[j].Ordinal
		}
		return cs[i].ID < cs[j].ID
	})
}

// Package model defines the domain types for the edge-decode workbench.
// Layers: immutable materials (captures, probe configs) vs. mutable
// decisions (parameter revisions, decode runs, human decisions, events).
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ---------- Immutable material layer ----------

// Channel describes one probed digital channel. Raw representation is
// preserved; normalization happens only in the derived layer.
type Channel struct {
	Alias    string `json:"alias"`             // human alias, e.g. "SDA"
	Probe    string `json:"probe"`             // probe id / physical pin
	Inverted bool   `json:"inverted"`          // level inversion applied at probe
	Role     string `json:"role"`              // sda|scl|sclk|mosi|miso|cs|clk|data
	CSIndex  int    `json:"cs_index,omitempty` // for multi-device chip selects
}

// Edge is a single digital transition. TimeRaw keeps the original
// representation (e.g. sample index string); TimeNS is the derived,
// normalized value. Edges that cannot be converted stay in place with a
// diagnostic attached to the capture.
type Edge struct {
	Channel string `json:"channel"`
	TimeRaw string `json:"time_raw"` // original representation, never rewritten
	TimeNS  int64  `json:"time_ns"`  // derived nanoseconds, 0 if unconvertible
	Level   int    `json:"level"`    // 0 or 1 after probe inversion is NOT applied; raw level
}

// Capture is an immutable imported material: edges + probe configuration.
type Capture struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	SampleRateHz  int64     `json:"sample_rate_hz"`
	ClockPolarity string    `json:"clock_polarity"` // "normal" | "inverted" as recorded at import
	Channels      []Channel `json:"channels"`
	Edges         []Edge    `json:"edges"`
	Diagnostics   []Diag    `json:"diagnostics,omitempty"`
}

// Diag is a positioned diagnostic. Incomplete input must surface as Diag,
// never as a silently defaulted value.
type Diag struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Channel  string `json:"channel,omitempty"`
	EdgeIdx  int    `json:"edge_idx,omitempty"`
	TimeNS   int64  `json:"time_ns,omitempty"`
	Severity string `json:"severity"` // "error" | "warn"
}

// Params is a versioned, immutable parameter set (decoder config).
type Params struct {
	ID             string `json:"id"`
	Version        int    `json:"version"`
	ParentID       string `json:"parent_id,omitempty"`
	DecoderVersion string `json:"decoder_version"`
	Protocol       string `json:"protocol"` // i2c | spi | sync
	// shared
	BoundaryNS    int64 `json:"boundary_ns"`    // edge-coincidence window for boundary sampling
	GapNS         int64 `json:"gap_ns"`         // data gap threshold; gaps are never stitched
	MaxCandidates int   `json:"max_candidates"` // resource budget
	MaxEdges      int   `json:"max_edges"`      // resource budget
	// i2c
	StretchNS int64 `json:"stretch_ns,omitempty"` // clock-stretch detection threshold
	// spi
	SPIMode string `json:"spi_mode,omitempty"` // "0".."3" or "unknown"
	// sync
	SyncBits int `json:"sync_bits,omitempty"`
}

// ---------- Derived layer ----------

// Decode status values. Budget exhaustion, corrupt format and semantic
// unsolvability are three distinct results.
const (
	StatusOK              = "ok"
	StatusBudgetExhausted = "budget_exhausted"
	StatusCorruptFormat   = "corrupt_format"
	StatusUnsolvable      = "unsolvable"
)

// Reason codes attached to candidates / events.
const (
	ReasonClockMissingEdge = "CLOCK_MISSING_EDGE"
	ReasonRepeatedStart    = "REPEATED_START"
	ReasonClockStretch     = "CLOCK_STRETCH"
	ReasonSPIModeUnknown   = "SPI_MODE_UNKNOWN"
	ReasonBoundarySample   = "BOUNDARY_SAMPLE"
	ReasonGapNoStitch      = "GAP_NO_STITCH"
	ReasonMultiCS          = "MULTI_CS_OVERLAP"
	ReasonParamRevision    = "PARAM_REVISION"
	ReasonSourceChange     = "SOURCE_CHANGE"
)

// Byte is one decoded byte with provenance back to raw edges.
type Byte struct {
	Value    int      `json:"value"`
	Ack      string   `json:"ack,omitempty"` // "ACK" | "NACK" | "" (i2c 9th bit)
	EdgeRefs []int    `json:"edge_refs"`     // indices into Capture.Edges
	StartNS  int64    `json:"start_ns"`
	EndNS    int64    `json:"end_ns"`
	Flags    []string `json:"flags,omitempty"`
}

// Candidate is one legal decode of a transaction. When the evidence does
// not uniquely determine a decode, all legal candidates are kept.
type Candidate struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons,omitempty"`
	Bytes      []Byte   `json:"bytes"`
	CS         string   `json:"cs,omitempty"` // chip-select alias for SPI
	Detail     string   `json:"detail,omitempty"`
}

// Transaction groups candidates decoded from one edge region.
type Transaction struct {
	ID         string      `json:"id"`
	Kind       string      `json:"kind"` // i2c | spi | sync
	StartNS    int64       `json:"start_ns"`
	EndNS      int64       `json:"end_ns"`
	Candidates []Candidate `json:"candidates"`
	Accepted   string      `json:"accepted,omitempty"` // candidate id accepted by decision
}

// Decode is one decode run: immutable result tied to inputs.
type Decode struct {
	ID             string        `json:"id"`
	CaptureID      string        `json:"capture_id"`
	ParamsID       string        `json:"params_id"`
	DecoderVersion string        `json:"decoder_version"`
	Status         string        `json:"status"`
	InputDigest    string        `json:"input_digest"`
	Transactions   []Transaction `json:"transactions,omitempty"`
	Diagnostics    []Diag        `json:"diagnostics,omitempty"`
	Seq            int64         `json:"seq"`
}

// ---------- Event log ----------

// Event kinds.
const (
	EvImportCapture = "import_capture"
	EvParamRevision = "param_revision"
	EvSourceChange  = "source_change" // new capture derived from one direct source change
	EvDecodeRun     = "decode_run"
	EvDecision      = "decision" // accept | reject | adjust
	EvExport        = "export"
	EvImport        = "import"
)

// Event is an append-only, machine-readable record of every judgment.
type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	ParentSeq int64           `json:"parent_seq"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// Decision payload: a human accept/reject/adjust judgment.
type Decision struct {
	ID          string `json:"id"`
	DecodeID    string `json:"decode_id"`
	Transaction string `json:"transaction_id"`
	CandidateID string `json:"candidate_id,omitempty"`
	Action      string `json:"action"` // accept | reject | adjust
	Note        string `json:"note,omitempty"`
}

// ---------- Identity & ordering ----------

// HashID derives a stable identity from content.
func HashID(prefix string, parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(h.Sum(nil))[:12])
}

// Digest computes the input digest recorded on every decode.
func Digest(parts ...any) string { return HashID("sha", parts...) }

// SortCandidates orders candidates deterministically so equivalent inputs
// yield a stable order: confidence desc, then canonical bytes, then id.
func SortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		bi, _ := json.Marshal(cs[i].Bytes)
		bj, _ := json.Marshal(cs[j].Bytes)
		if string(bi) != string(bj) {
			return string(bi) < string(bj)
		}
		return cs[i].ID < cs[j].ID
	})
}

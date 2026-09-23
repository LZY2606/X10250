// Package model defines the domain types for the edge-decoding workbench.
//
// Layering rule: imported materials (edges, channels, probe config) are
// immutable; everything computed carries an input digest, a parameter
// version and parent dependencies. Decisions are events, never rewrites.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// DecoderVersion is recorded on every decode run so historical conclusions
// keep the exact rule version that produced them.
const DecoderVersion = "1.4.2"

// ExportFormat identifies export documents.
const ExportFormat = "edgedesk-export"
const ExportVersion = 1

// Batch states. A batch is only visible after the committed marker is
// written; anything left in "writing" is half-written and recovered away.
const (
	BatchWriting   = "writing"
	BatchCommitted = "committed"
)

// Run statuses. Budget exhaustion, corrupt input and semantic no-solution
// are three distinct results, never collapsed into a generic failure.
const (
	RunOK              = "ok"
	RunBudgetExhausted = "budget_exhausted"
	RunCorrupt         = "corrupt"
	RunUnsolvable      = "unsolvable"
)

// Candidate statuses (current view; history lives in events).
const (
	CandPending  = "pending"
	CandAdopted  = "adopted"
	CandRejected = "rejected"
)

// Event kinds.
const (
	EvImport        = "import"
	EvParamRevise   = "param_revise"
	EvSourcePatch   = "source_patch"
	EvDecodeRun     = "decode_run"
	EvJudgment      = "judgment"
	EvAccept        = "accept"
	EvReject        = "reject"
	EvAdjust        = "adjust"
	EvExport        = "export"
	EvImportRestore = "import_restore"
)

// Batch is one imported capture: raw edges plus probe configuration.
type Batch struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	State          string `json:"state"`
	SampleRateHz   int64  `json:"sample_rate_hz"`
	TimeUnit       string `json:"time_unit"` // always "ps" internally
	DecoderVersion string `json:"decoder_version"`
	SourceRev      int    `json:"source_rev"`
	Note           string `json:"note"`
	CreatedAt      string `json:"created_at"`
}

// Channel is a probe configuration: alias, role, inversion, polarity.
type Channel struct {
	ID            string `json:"id"`
	BatchID       string `json:"batch_id"`
	Alias         string `json:"alias"`
	Role          string `json:"role"` // sda,scl,sclk,mosi,miso,cs0..,sync_clk,sync_data
	Inverted      bool   `json:"inverted"`
	ClockPolarity string `json:"clock_polarity"` // "" | "rising" | "falling"
	Position      int    `json:"position"`
}

// Edge is one raw digital transition. Tps is the original edge time in
// picoseconds; Level is the level as recorded (inversion is applied only
// in the derived layer).
type Edge struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Seq       int    `json:"seq"`
	Tps       int64  `json:"t_ps"`
	Level     int    `json:"level"`
	SourceRev int    `json:"source_rev"`
}

// ParamSet is the versioned decoder parameter set.
type ParamSet struct {
	I2CBoundaryWindowPs   int64 `json:"i2c_boundary_window_ps"`
	I2CStretchThresholdPs int64 `json:"i2c_stretch_threshold_ps"`
	GapThresholdPs        int64 `json:"gap_threshold_ps"`
	SPIMode               int   `json:"spi_mode"` // -1 = unknown, keep all candidates
	CSActiveHigh          bool  `json:"cs_active_high"`
	BitsPerWord           int   `json:"bits_per_word"`
	MaxEdges              int   `json:"max_edges"`
	MaxCandidates         int   `json:"max_candidates"`
}

// DefaultParams returns the baseline decoder parameters.
func DefaultParams() ParamSet {
	return ParamSet{
		I2CBoundaryWindowPs:   30_000,      // 30 ns
		I2CStretchThresholdPs: 20_000_000,  // 20 us
		GapThresholdPs:        200_000_000, // 200 us
		SPIMode:               -1,
		CSActiveHigh:          false,
		BitsPerWord:           8,
		MaxEdges:              100_000,
		MaxCandidates:         64,
	}
}

// Params is one immutable parameter version.
type Params struct {
	ID           string   `json:"id"`
	BatchID      string   `json:"batch_id"`
	Version      int      `json:"version"`
	ParentID     string   `json:"parent_id"`
	Set          ParamSet `json:"set"`
	CreatedEvent string   `json:"created_event"`
}

// Diagnostic is a positioned finding. Position is a channel, an edge or a
// time; incomplete input must produce these instead of silent defaults.
type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	ChannelID string `json:"channel_id,omitempty"`
	EdgeID    string `json:"edge_id,omitempty"`
	Tps       int64  `json:"t_ps,omitempty"`
	HasPos    bool   `json:"has_pos,omitempty"`
}

// Run is one decode pass over a (params, source revision, decoder version)
// triple.
type Run struct {
	ID             string       `json:"id"`
	BatchID        string       `json:"batch_id"`
	ParamID        string       `json:"param_id"`
	SourceRev      int          `json:"source_rev"`
	DecoderVersion string       `json:"decoder_version"`
	InputDigest    string       `json:"input_digest"`
	Status         string       `json:"status"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	CreatedEvent   string       `json:"created_event"`
	Seq            int64        `json:"seq"`
}

// Candidate is one legal decode interpretation. Ambiguity (missing clock
// edges, unknown SPI mode, boundary samples) is represented by keeping
// several candidates, never by silently picking one.
type Candidate struct {
	ID         string  `json:"id"`
	RunID      string  `json:"run_id"`
	Bus        string  `json:"bus"` // i2c | spi | sync
	Idx        int     `json:"idx"`
	Confidence float64 `json:"confidence"`
	Status     string  `json:"status"`
	Reason     string  `json:"reason"`
	Note       string  `json:"note"`
}

// Txn is one transaction inside a candidate.
type Txn struct {
	ID          string            `json:"id"`
	CandidateID string            `json:"candidate_id"`
	Idx         int               `json:"idx"`
	Kind        string            `json:"kind"`
	Device      string            `json:"device"`
	StartPs     int64             `json:"start_ps"`
	EndPs       int64             `json:"end_ps"`
	Meta        map[string]string `json:"meta"`
}

// ByteItem is one decoded byte/word with its provenance edge range.
type ByteItem struct {
	ID            string `json:"id"`
	TxnID         string `json:"txn_id"`
	Idx           int    `json:"idx"`
	Value         int    `json:"value"`
	HasValue      bool   `json:"has_value"`
	Ack           string `json:"ack"` // "" | "ACK" | "NACK"
	FirstEdgeID   string `json:"first_edge_id"`
	LastEdgeID    string `json:"last_edge_id"`
	Valid         bool   `json:"valid"`
	InvalidReason string `json:"invalid_reason"`
}

// Event is one entry in the append-only decision/derivation log.
type Event struct {
	ID             string `json:"id"`
	Seq            int64  `json:"seq"`
	BatchID        string `json:"batch_id"`
	Kind           string `json:"kind"`
	Payload        string `json:"payload"` // JSON, machine-readable
	ParentID       string `json:"parent_id"`
	ParamID        string `json:"param_id"`
	DecoderVersion string `json:"decoder_version"`
	Actor          string `json:"actor"`
	CreatedAt      string `json:"created_at"`
}

// Judgment is a machine-readable decoding decision basis, stored as an
// event payload (e.g. "data gap: not stitched", "SPI mode unknown: keep
// all mode candidates").
type Judgment struct {
	Rule    string            `json:"rule"`
	Action  string            `json:"action"`
	Subject string            `json:"subject"`
	Detail  map[string]string `json:"detail,omitempty"`
}

// HashID builds a deterministic stable identity from content parts.
func HashID(prefix string, parts ...any) string {
	h := sha256.New()
	h.Write([]byte(prefix))
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil))[:16]
}

// DigestOf hashes a canonical JSON encoding of the given values.
func DigestOf(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FormatPs renders a picosecond timestamp with a human unit, keeping the
// raw value authoritative.
func FormatPs(ps int64) string {
	switch {
	case ps%1_000_000_000_000 == 0:
		return fmt.Sprintf("%d s", ps/1_000_000_000_000)
	case ps%1_000_000_000 == 0:
		return fmt.Sprintf("%d ms", ps/1_000_000_000)
	case ps%1_000_000 == 0:
		return fmt.Sprintf("%d us", ps/1_000_000)
	case ps%1_000 == 0:
		return fmt.Sprintf("%d ns", ps/1_000)
	default:
		return fmt.Sprintf("%d ps", ps)
	}
}

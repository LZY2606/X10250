// Package model defines the immutable source materials (probe configuration,
// digital channel edges) and the derived review materials (decode runs,
// transactions, bytes, candidates, diagnostics, events).
//
// Layering rule: source materials carry their original textual representation
// and unit. Normalisation into picoseconds happens only in the derived layer.
// A value that cannot be converted is kept in place (time_ps == nil) and the
// parser attaches a positional diagnostic instead of silently dropping it.
package model

// Protocol identifiers understood by the decode bench.
const (
	ProtocolI2C    = "i2c"
	ProtocolSPI    = "spi"
	ProtocolCustom = "custom_sync"
)

// RawQuantity is a physical quantity exactly as imported, plus its normalised
// integer representation when conversion succeeded.
type RawQuantity struct {
	// Value is the original textual value ("10", "0.04", "12.5").
	Value string `json:"value"`
	// Unit is the original unit ("ps","ns","us","ms","s","Hz",...).
	Unit string `json:"unit"`
	// Ps is the normalised value in picoseconds; nil means the value could not
	// be converted and a diagnostic is attached at the same position.
	Ps *int64 `json:"time_ps,omitempty"`
}

// Channel is a probe/channel alias as declared in the import material.
type Channel struct {
	// Alias is the logical wire name ("SCL","SDA","SCK",...).
	Alias string `json:"alias"`
	// Probe is the physical probe/channel id ("CH0","P3").
	Probe string `json:"probe"`
	// Inverted records probe level inversion as imported. Effective levels are
	// computed as Level XOR Inverted; the original level is always retained.
	Inverted bool `json:"inverted"`
}

// Edge is one digital transition sample.
type Edge struct {
	// Seq is the 0-based coordinate of the edge inside the source fragment.
	Seq int `json:"seq"`
	// TimeRaw/Unit preserve the original time representation.
	TimeRaw string `json:"time"`
	Unit    string `json:"unit"`
	// Ps is the normalised time (picoseconds since capture origin); nil when
	// the original value could not be converted.
	Ps *int64 `json:"time_ps,omitempty"`
	// Channel alias the edge belongs to.
	Channel string `json:"channel"`
	// Level is the original sampled level (0/1) before inversion.
	Level int `json:"level"`
	// EffLevel is Level XOR Inverted; -1 when the channel alias is unknown.
	EffLevel int `json:"eff_level"`
}

// SPIConfig, I2CConfig and CustomConfig are protocol hints carried by the
// source material. None of them replace the versioned decode parameters; they
// only describe how the capture was taken.
type SPIConfig struct {
	CPOL     int  `json:"cpol"`
	CPHA     int  `json:"cpha"`
	ModeHint int  `json:"mode_hint"`
	ModeKnown bool `json:"mode_known"`
	BitOrder string `json:"bit_order"`
}

type I2CConfig struct {
	// AddressBits is kept for non-standard fragments (7 or 10).
	AddressBits int    `json:"address_bits"`
	PullUp      string `json:"pull_up"`
}

type CustomConfig struct {
	FrameBits int      `json:"frame_bits"`
	BitOrder  string   `json:"bit_order"`
	Roles     []string `json:"roles"`
}

// Capture is an immutable source fragment.
type Capture struct {
	ID               string       `json:"capture_id"`
	Name             string       `json:"name"`
	ParentID         string       `json:"parent_id,omitempty"`
	ChangeReason     string       `json:"change_reason,omitempty"`
	DecoderVersion   string       `json:"decoder_version"`
	ImporterVersion  string       `json:"importer_version"`
	SamplingInterval RawQuantity  `json:"sampling_interval"`
	SPI              *SPIConfig   `json:"spi,omitempty"`
	I2C              *I2CConfig   `json:"i2c,omitempty"`
	Custom           *CustomConfig `json:"custom,omitempty"`
	Channels         []Channel    `json:"channels"`
	Edges            []Edge       `json:"edges"`
	// SourceDigest is over the canonical imported bytes (edges + probe config).
	SourceDigest string `json:"source_digest"`
}

// Params is a versioned set of decode parameters for one bus.
type Params struct {
	Version  int    `json:"version"`
	BusID    string `json:"bus_id"`
	Protocol string `json:"protocol"`
	// SCL/SDA/SCK/MOSI/MISO aliases.
	SCL, SDA, SCK, MOSI, MISO string `json:"-"`
	RoleMap                  map[string]string `json:"role_map"`
	// CPOL/CPHA: nil means SPI mode is unknown at this parameter version.
	CPOL *int `json:"cpol,omitempty"`
	CPHA *int `json:"cpha,omitempty"`
	// SamplePoint: "pre" (sample just before the active clock edge) or
	// "boundary" (sample exactly on the edge coordinate).
	SamplePoint string `json:"sample_point"`
	BitOrder    string `json:"bit_order"`
	// CSActive maps CS alias -> active level after inversion (0 or 1).
	CSActive map[string]int `json:"cs_active"`
	// AddressBits for I2C (7/10).
	AddressBits int `json:"address_bits"`
	// FrameBits/Roles for the custom synchronous protocol.
	FrameBits int      `json:"frame_bits,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	// MaxGapPs: idle gap above which fragments must never be merged into one
	// transaction. 0 uses the decoder default (32 bit times).
	MaxGapPs int64 `json:"max_gap_ps,omitempty"`
}

// Diagnostic is an input-positioned finding. Severity is one of
// "error","warning","info". Code is a stable machine-readable identifier.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Position anchors the finding in source coordinates.
	Position Position `json:"position"`
}

// Position references material coordinates. All fields are optional so a
// diagnostic can point at an edge, a byte, a bus or the whole fragment.
type Position struct {
	CaptureID string `json:"capture_id,omitempty"`
	BusID     string `json:"bus_id,omitempty"`
	EdgeSeq   *int   `json:"edge_seq,omitempty"`
	TimePs    *int64 `json:"time_ps,omitempty"`
	ByteIndex *int   `json:"byte_index,omitempty"`
	Field     string `json:"field,omitempty"`
}

const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// Stable diagnostic codes.
const (
	DiagBadTime          = "BAD_TIME_VALUE"
	DiagBadLevel         = "BAD_LEVEL_VALUE"
	DiagUnknownChannel   = "UNKNOWN_CHANNEL_ALIAS"
	DiagDuplicateChannel = "DUPLICATE_CHANNEL_ALIAS"
	DiagMissingRole      = "MISSING_BUS_ROLE"
	DiagEdgesOutOfOrder  = "EDGES_OUT_OF_ORDER"
	DiagMissingClockEdge = "CLOCK_MISSING_EDGE"
	DiagRepeatedSTART    = "I2C_REPEATED_START"
	DiagClockStretching  = "I2C_CLOCK_STRETCHING"
	DiagDataGap          = "DATA_GAP_NOT_MERGED"
	DiagBoundarySample   = "BOUNDARY_SAMPLE_AMBIGUOUS"
	DiagMultiCS          = "MULTI_DEVICE_CHIP_SELECT"
	DiagAmbiguousSPIMode = "SPI_MODE_AMBIGUOUS"
	DiagNoSolution       = "SEMANTIC_NO_SOLUTION"
	DiagFormatCorrupt    = "FORMAT_CORRUPT"
	DiagBudgetExhausted  = "BUDGET_EXHAUSTED"
	DiagConflict         = "CONFLICTING_DECISION"
	DiagInvertedProbe    = "PROBE_LEVEL_INVERTED"
)

// Candidate is one legal decoding candidate for a candidate group.
type Candidate struct {
	ID         string  `json:"candidate_id"`
	GroupID    string  `json:"group_id"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	// Basis lists machine-readable evidence rule ids supporting the candidate.
	Basis []string `json:"basis"`
	// Value is the candidate's interpretation ("MODE0","MODE3", "READ","WRITE",
	// "STRETCH_TOLERATED",...).
	Value string `json:"value"`
	// Status: "pending" (default, retained alongside others), "accepted" or
	// "rejected" by a human decision.
	Status string `json:"status"`
	// DecidedBy/DecisionSeq record which decision changed the status.
	DecidedBy   string `json:"decided_by,omitempty"`
	DecisionSeq *int   `json:"decision_seq,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// ByteRec is one decoded byte with its edge span and ack information.
type ByteRec struct {
	// StableID is stable across export/import (content-derived).
	StableID string `json:"byte_id"`
	Index    int    `json:"index"`
	Value    *int   `json:"value"` // nil => not decodable; never faked
	Hex      string `json:"hex"`
	Role     string `json:"role"` // ADDRESS, DATA, FRAME
	// Ack: "A","N","" (unknown). I2C only.
	Ack string `json:"ack,omitempty"`
	// StartPs/EndPs delimit the contributing edges.
	StartPs *int64 `json:"start_ps"`
	EndPs   *int64 `json:"end_ps"`
	// EdgeSeqs are the source coordinates of every contributing edge, so any
	// displayed byte can be read back to edges.
	EdgeSeqs []int `json:"edge_seqs"`
	// CandidateGroupID != "" when the byte has competing legal readings.
	CandidateGroupID string `json:"candidate_group_id,omitempty"`
}

// Transaction is one (never gap-merged) bus transaction.
type Transaction struct {
	StableID string `json:"transaction_id"`
	Index    int    `json:"index"`
	BusID    string `json:"bus_id"`
	Kind     string `json:"kind"` // I2C_WRITE/I2C_READ/SPI_FRAME/CUSTOM_FRAME/...
	StartPs  *int64 `json:"start_ps"`
	EndPs    *int64 `json:"end_ps"`
	EdgeSeqs []int  `json:"edge_seqs"`
	Bytes    []ByteRec `json:"bytes"`
	// CS records the chip-select observation for SPI (alias + active level).
	CS string `json:"cs,omitempty"`
	// Complete is false when the fragment is truncated/malformed.
	Complete bool `json:"complete"`
}

// Run is the output of decoding one bus from one capture under one parameter
// version. It is itself immutable: parameter revisions create new runs.
type Run struct {
	ID            string       `json:"run_id"`
	CaptureID     string       `json:"capture_id"`
	BusID         string       `json:"bus_id"`
	ParamsVersion int          `json:"params_version"`
	Decoder       string       `json:"decoder"`
	DecoderVersion string      `json:"decoder_version"`
	// InputSummary is the digest of the exact (capture, params) slice used.
	InputSummary InputSummary `json:"input_summary"`
	// Status: "ok","ok_with_diagnostics","semantic_no_solution","budget_exhausted".
	Status      string        `json:"status"`
	Transactions []Transaction `json:"transactions"`
	Candidates   []Candidate   `json:"candidates"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
}

// InputSummary pins a derived result to its inputs.
type InputSummary struct {
	SourceDigest  string `json:"source_digest"`
	CaptureID     string `json:"capture_id"`
	ParamsVersion int    `json:"params_version"`
	BusID         string `json:"bus_id"`
	EdgeCount     int    `json:"edge_count"`
	MinPs         *int64 `json:"min_ps"`
	MaxPs         *int64 `json:"max_ps"`
	ParamsDigest  string `json:"params_digest"`
}

// Budget bounds decoder work so hostile inputs cannot consume unbounded
// resources.
type Budget struct {
	MaxEdges      int `json:"max_edges"`
	MaxRuns       int `json:"max_runs"`
	MaxCandidates int `json:"max_candidates_per_group"`
}

// DefaultBudget is used unless the caller overrides it.
func DefaultBudget() Budget {
	return Budget{MaxEdges: 500_000, MaxRuns: 64, MaxCandidates: 16}
}

// Outcome classes for import/decode. They must stay mutually distinct.
const (
	OutcomeOK               = "ok"
	OutcomeFormatCorrupt    = "format_corrupt"
	OutcomeBudgetExhausted  = "budget_exhausted"
	OutcomeSemanticNoSolution = "semantic_no_solution"
)

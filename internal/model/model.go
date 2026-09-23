// Package model holds the immutable evidence, derived-result and mutable
// decision types for the edge decoding bench.
//
// Layering:
//
//	Capture  = immutable material (raw edges, probe config, original text)
//	Params   = decoder parameter versions (immutable once stored)
//	Run      = derived layer: every run records its input summary, parameter
//	           version and parent dependencies and is never overwritten
//	Decision = reviewer-created mutable layer (accept / reject / retract)
//	Event    = append-only machine-readable journal of all of the above
package model

import "time"

// Severity levels for diagnostics.
type Severity string

const (
	SevInfo    Severity = "info"
	SevWarning Severity = "warning"
	SevError   Severity = "error"
)

// Status is the three-way terminal classification required by the spec:
// resource budget exhaustion, corrupt format and semantic impossibility are
// always different results (plus ok).
type Status string

const (
	StatusOK              Status = "ok"
	StatusCorrupt         Status = "corrupt"          // malformed input representation
	StatusSemanticImpasse Status = "semantic_impasse" // well-formed but impossible to decode
	StatusBudgetExhausted Status = "budget_exhausted" // resource limit hit before completion
)

// Diag is a positioned diagnostic. Positions are 1-based (line,col) for
// source text and a symbolic path for derived structures.
type Diag struct {
	Code     string    `json:"code"`
	Severity Severity  `json:"severity"`
	Message  string    `json:"message"`
	Line     int       `json:"line,omitempty"`
	Col      int       `json:"col,omitempty"`
	Path     string    `json:"path,omitempty"`
	Kind     Status    `json:"kind,omitempty"`
}

// Quantity keeps the original textual representation together with the
// normalized numeric value and unit. Normalized is only present when the
// value could actually be converted; otherwise Raw is preserved in place
// with a diagnostic.
type Quantity struct {
	Raw        string  `json:"raw"`
	Normalized float64 `json:"normalized,omitempty"`
	Unit       string  `json:"unit,omitempty"` // canonical unit, "" when unconvertible
	OK         bool    `json:"ok"`
}

// Edge is a raw level transition on a physical channel. Time carries the
// original representation plus a normalized value in seconds.
type Edge struct {
	Seq    int      `json:"seq"` // 0-based order within the capture
	Ch     string   `json:"ch"`
	Time   Quantity `json:"time"`
	Level  int      `json:"level"` // raw wire level before polarity handling
	Line   int      `json:"line,omitempty"`
}

// Channel is the probe configuration for one physical channel.
type Channel struct {
	Name     string   `json:"name"`
	Alias    string   `json:"alias,omitempty"`
	Inverted bool     `json:"inverted"`
	Initial  int      `json:"initial"` // declared initial raw level
	Role     string   `json:"role,omitempty"`
	Diags    []Diag   `json:"diags,omitempty"`
}

// Capture is the immutable material layer.
type Capture struct {
	ID         string            `json:"id"`
	Format     string            `json:"format"` // "edgebench-capture"
	FormatVer  string            `json:"format_version"`
	Device     string            `json:"device"`
	SampleRate Quantity          `json:"sample_rate"`
	Channels   []Channel         `json:"channels"`
	Edges      []Edge            `json:"edges"`
	Raw        string            `json:"raw"`
	Diags      []Diag            `json:"diags,omitempty"`
	Headers    map[string]string `json:"headers,omitempty"`
	ImportedAt time.Time         `json:"imported_at"`
}

// Params is an immutable decoder parameter version.
type Params struct {
	Version       int    `json:"version"`
	ParentVersion int    `json:"parent_version"`
	DecoderID     string `json:"decoder_id"`
	DecoderVer    string `json:"decoder_version"`

	// I2C
	I2CClockPol string `json:"i2c_clock_pol"` // "idle_high" (standard) | "inverted"
	I2CSampleAt string `json:"i2c_sample_at"` // "rising" | "falling" | "boundary"

	// SPI
	SPIClockPol string `json:"spi_clock_pol"` // "low" | "high"
	SPIClockPha string `json:"spi_clock_pha"` // "leading" | "trailing"
	SPIMode     string `json:"spi_mode"`      // "0".."3" | "unknown"
	SPICSActive string `json:"spi_cs_active"` // "low" | "high"

	// Generic
	StretchS    float64 `json:"stretch_seconds"` // SCL low longer than this => stretching
	GapS        float64 `json:"gap_seconds"`     // no activity longer than this => gap
	MaxCandidates int   `json:"max_candidates"`
	MaxEdges      int   `json:"max_edges"`
	Reason        string `json:"reason"`
}

// InputDigest is the input summary recorded by every derived run.
type InputDigest struct {
	CaptureID   string `json:"capture_id"`
	EdgeCount   int    `json:"edge_count"`
	EdgeFirstS  string `json:"edge_first_s"`
	EdgeLastS   string `json:"edge_last_s"`
	ChannelHash string `json:"channel_hash"`
	RateRaw     string `json:"rate_raw"`
}

// EdgeRef points back to a raw edge; it makes every displayed byte
// traceable back to the wire.
type EdgeRef struct {
	Seq  int    `json:"seq"`
	Ch   string `json:"ch"`
	TimeS string `json:"time_s"`
}

// Item is a decoded byte / bit fragment.
type Item struct {
	ID         string          `json:"id"`
	Protocol   string          `json:"protocol"` // i2c | spi | sync
	Kind       string          `json:"kind"`     // address|data|partial|...
	Bus        string          `json:"bus,omitempty"`
	Device     string          `json:"device,omitempty"`
	BitOrder   string          `json:"bit_order,omitempty"`
	StartEdges []EdgeRef       `json:"start_edges"`
	EndEdges   []EdgeRef       `json:"end_edges"`
	StartS     string          `json:"start_s"`
	EndS       string          `json:"end_s"`
	Candidates []ByteCandidate `json:"candidates"`
	Accepted   string          `json:"accepted,omitempty"` // candidate value or ""
	Confidence string          `json:"confidence"`         // unique | ambiguous | none
	Ack        string          `json:"ack,omitempty"`      // ack | nack | unknown | ""
	Complete   bool            `json:"complete"`
	Note       string          `json:"note,omitempty"`
}

// ByteCandidate is one legal interpretation of a bit window.
type ByteCandidate struct {
	Value      string  `json:"value"` // "0x.."
	Bits       string  `json:"bits,omitempty"`
	Score      float64 `json:"score"`
	Reason     string  `json:"reason"`
	Ambiguous  bool    `json:"ambiguous"`
	Mode       string  `json:"mode,omitempty"` // SPI mode label e.g. "mode0"
}

// Marker is a structured bus event (START, STOP, repeated START, stretch,
// missing edge, gap, cs...).
type Marker struct {
	ID     string    `json:"id"`
	Kind   string    `json:"kind"`
	Bus    string    `json:"bus,omitempty"`
	Device string    `json:"device,omitempty"`
	StartS string    `json:"start_s"`
	EndS   string    `json:"end_s,omitempty"`
	Edges  []EdgeRef `json:"edges"`
	Detail string    `json:"detail,omitempty"`
}

// Transaction is a reassembled bus transaction; gaps are never spliced.
type Transaction struct {
	ID       string   `json:"id"`
	Protocol string   `json:"protocol"`
	Bus      string   `json:"bus,omitempty"`
	Device   string   `json:"device,omitempty"`
	ItemIDs  []string `json:"item_ids"`
	StartS   string   `json:"start_s"`
	EndS     string   `json:"end_s"`
	Spliced  bool     `json:"spliced"`
	Complete bool     `json:"complete"`
	Note     string   `json:"note,omitempty"`
}

// Run is an immutable derived result.
type Run struct {
	ID         string        `json:"id"`
	ProjectID  string        `json:"project_id"`
	CaptureID  string        `json:"input_capture_id"`
	ParamsVer  int           `json:"params_version"`
	DecoderID  string        `json:"decoder_id"`
	DecoderVer string        `json:"decoder_version"`
	ParentRun  string        `json:"parent_run_id,omitempty"`
	Reason     string        `json:"reason"`
	Status     Status        `json:"status"`
	Input      InputDigest   `json:"input"`
	Items      []Item        `json:"items"`
	Markers    []Marker      `json:"markers"`
	Trans      []Transaction `json:"transactions"`
	Diags      []Diag        `json:"diags,omitempty"`
	CreatedAt  time.Time     `json:"created_at"`
}

// Event is the append-only, machine-readable journal record.
type Event struct {
	Seq       int            `json:"seq"`
	At        time.Time      `json:"at"`
	Type      string         `json:"type"` // capture_imported|params_created|run_decoded|decision|...
	ProjectID string         `json:"project_id"`
	ObjectID  string         `json:"object_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	Diags     []Diag         `json:"diags,omitempty"`
}

// Decision is the reviewer-mutable layer: accept/reject/adjust.
type Decision struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	RunID      string    `json:"run_id"`
	ItemID     string    `json:"item_id,omitempty"`
	Kind       string    `json:"kind"` // accept|reject|retract|param_adjust|...
	Chosen     string    `json:"chosen,omitempty"`
	Reason     string    `json:"reason"`
	ParamsVer  int       `json:"params_version,omitempty"`
	Supersedes string    `json:"supersedes,omitempty"`
	EventSeq   int       `json:"event_seq"`
	CreatedAt  time.Time `json:"created_at"`
}

// View is a run overlaid with decisions visible at a point in history.
type View struct {
	AtEventSeq  int           `json:"at_event_seq"`
	Run         Run           `json:"run"`
	Decisions   []Decision    `json:"decisions"`
	Conflicts   []Diag        `json:"conflicts,omitempty"`
	Current     bool          `json:"current"`
}

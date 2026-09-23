// Package model contains the domain types shared by ingest, decoding,
// persistence and presentation. Types here are serializable directly; the
// persistence layer stores them verbatim and the business rules live in Go,
// not in SQL or browser scripts.
package model

import "edgebench/internal/qty"

// DecoderVersion is the set of decoder rule versions applied to a run.
type DecoderVersion struct {
	I2C  string `json:"i2c"`
	SPI  string `json:"spi"`
	Sync string `json:"sync"`
}

// Channel is one physical/logic probe channel as configured by the hardware team.
type Channel struct {
	// Index is the physical channel position (0-based coordinate on the probe).
	Index int `json:"index"`
	// Alias is the human name; defaults are never invented, it is required.
	Alias string `json:"alias"`
	// Invert is the probe-level level inversion configured at import.
	Invert bool `json:"invert"`
}

// Edge is one digital transition. EdgeID is unique within a capture and is
// preserved through every derived layer so any byte can return to its edges.
type Edge struct {
	EdgeID  string       `json:"edge_id"`
	Channel string       `json:"channel"`
	// Seq is the per-channel ordinal (stable coordinate of the edge).
	Seq int `json:"seq"`
	// Time keeps raw text, unit and normalized picoseconds.
	Time qty.Quantity `json:"time"`
	// Level is the post-inversion logic level (0/1).
	Level int `json:"level"`
	// RawLevel is the level reported by the probe before inversion.
	RawLevel int `json:"raw_level"`
}

// ValidRange marks the normalized picosecond span that produced a result.
type ValidRange struct {
	StartPS int64 `json:"start_ps"`
	EndPS   int64 `json:"end_ps"`
}

// Capture is the immutable imported material (edges + probe configuration).
type Capture struct {
	CaptureID   string             `json:"capture_id"`
	Name        string             `json:"name"`
	SampleRate  qty.Quantity       `json:"sample_rate"`
	Channels    []Channel          `json:"channels"`
	Edges       []Edge             `json:"edges"`
	Buses       []Bus              `json:"buses"`
	Diagnostics []Diagnostic       `json:"diagnostics,omitempty"`
}

// BusKind enumerates supported reorganizations.
type BusKind string

const (
	BusI2C  BusKind = "i2c"
	BusSPI  BusKind = "spi"
	BusSync BusKind = "sync"
)

// Bus maps protocol roles to channel aliases.
type Bus struct {
	BusID   string  `json:"bus_id"`
	Kind    BusKind `json:"kind"`
	SCL     string  `json:"scl,omitempty"`
	SDA     string  `json:"sda,omitempty"`
	MOSI    string  `json:"mosi,omitempty"`
	MISO    string  `json:"miso,omitempty"`
	// Clock is the synchronous clock alias for custom sync buses.
	Clock string `json:"clock,omitempty"`
	Data  string `json:"data,omitempty"`
	// CS maps chip-select alias -> device label (SPI / custom framed sync).
	CS map[string]string `json:"cs,omitempty"`
	// ClockPolarity is the import-time probe-level clock polarity
	// ("normal" / "inverted"); it is part of the immutable material.
	ClockPolarity string `json:"clock_polarity,omitempty"`
}

// Severity of a diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Diagnostic carries a stable code and a location (JSON-ish pointer or
// bus/edge coordinate) so incomplete input never looks like success.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Pos      string   `json:"pos,omitempty"`
	Range    *ValidRange `json:"range,omitempty"`
}

// Params is the mutable decision layer: decoder parameters at a version.
type Params struct {
	ParamID string `json:"param_id"`
	Label   string `json:"label"`
	// I2CClockPolarity is "normal" or "inverted" (logical view).
	I2CClockPolarity string `json:"i2c_clock_polarity"`
	// SPIMode: -1 = unknown (keep all four candidates), 0..3 pinned.
	SPIMode int `json:"spi_mode"`
	// BoundaryPolicy: "v1" (sample at declared instant, strict) or
	// "v2" (prefer pre-edge level on simultaneous transitions).
	BoundaryPolicy string `json:"boundary_policy"`
	// DataGapPS is the idle gap at which a stream must not be stitched.
	DataGapPS int64 `json:"data_gap_ps"`
	// MaxCandidates bounds parallel candidates when data is ambiguous.
	MaxCandidates int `json:"max_candidates"`
}

// Byte is a decoded nibble/byte/ack at a stable position with edge-level
// provenance. Value uses 0..255 for bytes; for ACK/NACK slots Kind is set.
type Byte struct {
	ByteID   string `json:"byte_id"`
	BusID    string `json:"bus_id"`
	Index    int    `json:"index"`
	Kind     string `json:"kind"` // "address","data","ack","nack","spi","sync","partial"
	Value    int    `json:"value"`
	BitOrder string `json:"bit_order,omitempty"`
	Range    ValidRange `json:"range"`
	// ClockEdges/SampleEdges are the exact edges the value was read from.
	ClockEdges  []string `json:"clock_edges"`
	SampleEdges []string `json:"sample_edges"`
	Ack         *bool    `json:"ack,omitempty"`
	// Preferred marks the chosen value when Candidates has more than one entry.
	Preferred bool `json:"preferred"`
}

// Segment is one START..STOP (or chip-select frame) part of a transaction.
type Segment struct {
	SegmentID string    `json:"segment_id"`
	Device    string    `json:"device,omitempty"`
	CS        string    `json:"cs,omitempty"`
	Range     ValidRange `json:"range"`
	ByteIDs   []string  `json:"byte_ids"`
}

// CandidateKind selects what an ambiguity applies to.
type CandidateKind string

const (
	CandByte        CandidateKind = "byte"
	CandTransaction CandidateKind = "transaction"
)

// Candidate is one legal interpretation retained because the evidence does not
// uniquely decide. Confidence is 0..1; Reason is a machine-readable code.
type Candidate struct {
	CandidateID string        `json:"candidate_id"`
	Kind        CandidateKind `json:"kind"`
	TargetID    string        `json:"target_id"`
	Label       string        `json:"label"`
	Confidence  float64       `json:"confidence"`
	Reason      string        `json:"reason"`
	Preferred   bool          `json:"preferred"`
}

// Transaction groups one or more segments. GapSplit marks transactions that
// were deliberately not stitched across a data gap.
type Transaction struct {
	TxnID      string       `json:"txn_id"`
	BusID      string       `json:"bus_id"`
	Ordinal    int          `json:"ordinal"`
	Kind       BusKind      `json:"kind"`
	Device     string       `json:"device,omitempty"`
	Segments   []Segment    `json:"segments"`
	Range      ValidRange   `json:"range"`
	Complete   bool         `json:"complete"`
	GapSplit   bool         `json:"gap_split,omitempty"`
}

// Provenance links every computed result to its inputs and rule versions.
type Provenance struct {
	CaptureID       string          `json:"capture_id"`
	CaptureSummary  string          `json:"capture_summary"`
	ParamID         string          `json:"param_id"`
	ParamsDigest    string          `json:"params_digest"`
	DecoderVersions DecoderVersion  `json:"decoder_versions"`
	// Parents lists immediate upstream dependencies (event ids / run ids).
	Parents []string `json:"parents"`
}

// Run is one immutable decode computation.
type Run struct {
	RunID       string       `json:"run_id"`
	Status      string       `json:"status"` // "ok","semantic_rejected","budget_exhausted","format_corrupt"
	Provenance  Provenance   `json:"provenance"`
	Transactions []Transaction `json:"transactions"`
	Bytes       []Byte       `json:"bytes"`
	Candidates  []Candidate  `json:"candidates"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// EventKind enumerates machine-readable timeline events.
type EventKind string

const (
	EvImport            EventKind = "capture.imported"
	EvParams            EventKind = "params.revised"
	EvRun               EventKind = "run.computed"
	EvReject            EventKind = "input.rejected"
	EvDecision          EventKind = "decision.recorded"
	EvJudgement         EventKind = "judgement.recorded"
	EvExport            EventKind = "project.exported"
	EvImportProject     EventKind = "project.imported"
	EvGap               EventKind = "datagap.detected"
	EvMissingClock      EventKind = "clock.missing_edge"
	EvRepeatedStart     EventKind = "i2c.repeated_start"
	EvStretch           EventKind = "i2c.clock_stretch"
	EvSPIModeAmbiguous  EventKind = "spi.mode_ambiguous"
)

// Event is an append-only, machine-readable timeline entry. Returning to an old
// event restores dependency versions and visible conclusions of that moment.
type Event struct {
	EventID    string            `json:"event_id"`
	Seq        int64             `json:"seq"`
	Kind       EventKind         `json:"kind"`
	AtPS       int64             `json:"at_ps,omitempty"`
	Summary    string            `json:"summary"`
	Subjects   []string          `json:"subjects"`
	Payload    map[string]string `json:"payload,omitempty"`
	// Parents are the direct sources this event derived from.
	Parents []string `json:"parents"`
}

// Verdict of a human decision.
type Verdict string

const (
	VerdictAccept Verdict = "accept"
	VerdictReject Verdict = "reject"
	VerdictAdjust Verdict = "adjust"
)

// Decision is the mutable, human layer. Decisions never rewrite old results.
type Decision struct {
	DecisionID  string            `json:"decision_id"`
	Verdict     Verdict           `json:"verdict"`
	TargetKind  string            `json:"target_kind"` // "candidate","transaction","run"
	TargetID    string            `json:"target_id"`
	Reason      string            `json:"reason"`
	NewParamID  string            `json:"new_param_id,omitempty"`
}

// Package model defines the core domain types of the edge decoding bench:
// immutable capture material, versioned decoder parameters, decoded
// candidates with byte provenance, structured evidence and the append-only
// event log.
package model

import "time"

// Bus kinds.
const (
	BusI2C    = "i2c"
	BusSPI    = "spi"
	BusCustom = "custom_sync"
)

// Edge is an immutable digital transition. Offset is the offset from the
// capture time origin, stored as an exact rational number of nanoseconds so
// the original representation is never rounded away.
type Edge struct {
	ID         string `json:"id"`
	Channel    string `json:"channel"`
	LogicalCh  string `json:"channel_logical"`
	Sample     int64  `json:"sample"`
	OffsetNs   *Rat   `json:"offset_ns"`
	OffsetText string `json:"offset_text"`
	Level      int    `json:"level"`
	// LevelRaw is the raw probe level before inversion was applied.
	LevelRaw int `json:"level_raw"`
	// Synthesized marks edges produced by a source-data amendment; BaseEdge
	// points at the edge that occupied the same logical position (if any).
	Synthesized bool   `json:"synthesized,omitempty"`
	BaseEdge    string `json:"base_edge,omitempty"`
}

// Channel describes one digital probe channel. Alias and inversion are part
// of the immutable capture material; the decoder parameter layer may revise
// them and that revision is versioned.
type Channel struct {
	Name       string `json:"name"`
	Alias      string `json:"alias"`
	Inverted   bool   `json:"inverted"`
	InvertText string `json:"invert_text"`
}

// Bus binds logical roles (scl,sda,...) to channel names.
type Bus struct {
	Kind  string            `json:"kind"`
	Name  string            `json:"name"`
	Roles map[string]string `json:"roles"`
}

// Probe is the immutable probe configuration snapshot.
type Probe struct {
	RateHz   int64     `json:"rate_hz"`
	RateText string    `json:"rate_text"`
	Unit     string    `json:"unit"` // Hz
	Channels []Channel `json:"channels"`
	Buses    []Bus     `json:"buses"`
}

// Capture is the fully immutable import material.
type Capture struct {
	ID         string         `json:"id"`
	FormatVer  string         `json:"format_ver"`
	SourceName string         `json:"source_name"`
	ImportedAt time.Time      `json:"imported_at"`
	TimeOrigin string         `json:"time_origin"` // e.g. t0=0ns, ISO8601 when present
	TimeUnit   string         `json:"time_unit"`
	Probe      Probe          `json:"probe"`
	Edges      []Edge         `json:"edges"`
	InputText  string         `json:"input_text"`
	Diags      []Diagnostic   `json:"diagnostics"`
	SourceRefs []SourceRef    `json:"source_refs"`
	Extra      map[string]any `json:"extra,omitempty"`
}

// SourceRef records where a capture value came from in the original input
// (line/column offsets), used by positional diagnostics.
type SourceRef struct {
	Kind    string `json:"kind"` // rate|alias|invert|edge|polarity|...
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	EdgeID  string `json:"edge_id,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// Params is a versioned, mutable decoder decision layer.
type Params struct {
	Version        int               `json:"version"`
	ParentVersion  int               `json:"parent_version"`
	DecoderVer     string            `json:"decoder_ver"`
	ClockPolarity  map[string]string `json:"clock_polarity"` // bus -> normal|inverted|unknown
	ClockPolarityT map[string]string `json:"clock_polarity_text"`
	SampleBoundary string            `json:"sample_boundary"` // last|first
	GapToleranceNs *Rat              `json:"gap_tolerance_ns"`
	SyncPolarity   map[string]string `json:"sync_polarity"` // custom bus -> high|low
	Aliases        map[string]string `json:"aliases"`       // channel -> alias override
	Inversions     map[string]bool   `json:"inversions"`    // channel -> inverted override
	Notes          string            `json:"notes,omitempty"`
}

// Byte is one decoded byte with stable identity and edge provenance.
type Byte struct {
	ID         string   `json:"id"`
	Bus        string   `json:"bus"`
	Index      int      `json:"index"`
	Value      int      `json:"value"`
	ValueText  string   `json:"value_text"`
	Role       string   `json:"role"` // addr|data|...
	AckNack    string   `json:"ack_nack,omitempty"`
	EdgeIDs    []string `json:"edge_ids"`
	StartEdge  string   `json:"start_edge"`
	EndEdge    string   `json:"end_edge"`
	StartNs    *Rat     `json:"start_ns"`
	EndNs      *Rat     `json:"end_ns"`
	SampleMode string   `json:"sample_mode"`
	Valid      bool     `json:"valid"`
	InvalidWhy string   `json:"invalid_why,omitempty"`
}

// Segment is an I2C-style START..(rSTART)..STOP piece or a CS frame.
type Segment struct {
	ID        string   `json:"id"`
	Bus       string   `json:"bus"`
	Kind      string   `json:"kind"` // i2c_segment|spi_frame|custom_frame
	StartEdge string   `json:"start_edge"`
	EndEdge   string   `json:"end_edge"`
	StartNs   *Rat     `json:"start_ns"`
	EndNs     *Rat     `json:"end_ns"`
	ByteIDs   []string `json:"byte_ids"`
	Device    string   `json:"device,omitempty"` // CS channel / address
}

// Transaction is a reassembled bus transaction. Gaps are never stitched.
type Transaction struct {
	ID          string   `json:"id"`
	Bus         string   `json:"bus"`
	SegmentIDs  []string `json:"segment_ids"`
	ByteIDs     []string `json:"byte_ids"`
	StartEdge   string   `json:"start_edge"`
	EndEdge     string   `json:"end_edge"`
	StartNs     *Rat     `json:"start_ns"`
	EndNs       *Rat     `json:"end_ns"`
	StitchedGap bool     `json:"stitched_gap"`
}

// Evidence is a machine-readable structured state event produced by the
// decoder (missing clock edge, repeated START, clock stretch, retained
// candidate, gap that prevents stitching, ...).
type Evidence struct {
	Code      string         `json:"code"`
	Bus       string         `json:"bus,omitempty"`
	Severity  string         `json:"severity"` // info|warn|error
	StartEdge string         `json:"start_edge,omitempty"`
	EndEdge   string         `json:"end_edge,omitempty"`
	StartNs   *Rat           `json:"start_ns,omitempty"`
	EndNs     *Rat           `json:"end_ns,omitempty"`
	EdgeIDs   []string       `json:"edge_ids,omitempty"`
	Message   string         `json:"message"`
	Detail    map[string]any `json:"detail,omitempty"`
}

// Candidate is one legal decoding result. When the input does not determine
// a unique answer, every legal candidate is retained.
type Candidate struct {
	ID           string            `json:"id"`
	Bus          string            `json:"bus"`
	Hypothesis   map[string]string `json:"hypothesis"` // e.g. cpol=0,cpha=0 / polarity=normal
	Confidence   float64           `json:"confidence"`
	Accepted     bool              `json:"accepted"`
	Rejected     bool              `json:"rejected"`
	DecisionNote string            `json:"decision_note,omitempty"`
	Bytes        []Byte            `json:"bytes"`
	Segments     []Segment         `json:"segments"`
	Transactions []Transaction     `json:"transactions"`
	Evidence     []Evidence        `json:"evidence"`
	Diagnostics  []Diagnostic      `json:"diagnostics"`
	GroupKey     string            `json:"group_key"` // equivalent region grouping
}

// Derivation is a content-addressed computation record.
type Derivation struct {
	Key         string       `json:"key"`
	InputDigest string       `json:"input_digest"`
	ParamsVer   int          `json:"params_version"`
	RuleVer     string       `json:"rule_version"`
	Parents     []string     `json:"parents"`
	CaptureID   string       `json:"capture_id"`
	Candidates  []Candidate  `json:"candidates"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	ComputedAt  time.Time    `json:"computed_at"`
}

// Diagnostic is a positional diagnostic. Nothing is ever silently dropped.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error|warn|info
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Channel  string `json:"channel,omitempty"`
	EdgeID   string `json:"edge_id,omitempty"`
	Message  string `json:"message"`
	// RawValue preserves a value that could not be normalized.
	RawValue string `json:"raw_value,omitempty"`
	// Retained says the unconvertible value stayed in place.
	Retained bool `json:"retained,omitempty"`
}

// Event types.
const (
	EvImport     = "capture_imported"
	EvParams     = "params_revised"
	EvAmend      = "source_amended"
	EvDecision   = "decision_made"
	EvBranch     = "branch_created"
	EvDerivation = "derivation_recorded"
)

// Event is one append-only log entry.
type Event struct {
	Seq           int64          `json:"seq"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Branch        string         `json:"branch"`
	ParentEvent   string         `json:"parent_event"`
	Timestamp     time.Time      `json:"timestamp"`
	ProjectID     string         `json:"project_id"`
	Payload       map[string]any `json:"payload"`
	DeriveKey     string         `json:"derive_key,omitempty"`
	ConflictsWith []string       `json:"conflicts_with,omitempty"`
}

// Project bundles metadata for an export/import unit.
type Project struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	HeadBranch string    `json:"head_branch"`
	ExportVer  string    `json:"export_ver"`
}

// Branch is a named fork at an event.
type Branch struct {
	Name      string    `json:"name"`
	ForkEvent string    `json:"fork_event"`
	CreatedAt time.Time `json:"created_at"`
}

// Decision is the human accept/reject/adjust payload.
type Decision struct {
	CandidateID string            `json:"candidate_id"`
	Bus         string            `json:"bus"`
	Action      string            `json:"action"` // accept|reject|adjust
	Adjust      map[string]string `json:"adjust,omitempty"`
	Note        string            `json:"note,omitempty"`
	Accepted    bool              `json:"accepted,omitempty"`
	Rejected    bool              `json:"rejected,omitempty"`
}

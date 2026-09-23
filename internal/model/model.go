// Package model defines the layered data model of the edge decoding bench.
//
// Layers:
//
//   - Material layer (Capture, Edge, ChAlias...): imported facts that are never
//     overwritten. A source change creates a new capture revision instead.
//   - Decision/parameter layer (Params, Decision): reviewer choices and decoder
//     parameter sets, both versioned through the event tree.
//   - Derived layer (DecodeResult, FrameCandidate, ByteValue): recomputable
//     output recording an input summary, decoder version and parent dependency.
package model

import (
	"encoding/json"
	"time"
)

// DecoderVersion is the rule-set version stamped on every imported capture and
// every derived result. Bumping it changes result identities so history is
// never silently overwritten by newer calculations.
const DecoderVersion = "edgebench-decoder-1.0.0"

// Result/import statuses. Resource exhaustion, corrupt format and semantic
// unsolvability are deliberately three distinct outcomes.
const (
	StatusDecoded            = "decoded"
	StatusIncomplete         = "incomplete_input"
	StatusResourceExhausted  = "resource_exhausted"
	StatusSemanticUnsolvable = "semantic_unsolvable"
	StatusImportCorrupt      = "import_corrupt"
)

// Orig is a physical quantity exactly as written by the importer. Text is
// always retained; SI is the normalized SI-base value (seconds, hertz) and is
// only valid when OK is true. Unconvertible values stay in place with a
// diagnostic instead of being dropped or defaulted.
type Orig struct {
	Text string  `json:"text"`
	SI   float64 `json:"si,omitempty"`
	Unit string  `json:"unit,omitempty"`
	OK   bool    `json:"ok"`
	Diag string  `json:"diag,omitempty"`
}

// Edge is one digital transition. Level may be nil when the source only
// records a transition ("toggle"): the polarity then stays unresolved rather
// than being guessed.
type Edge struct {
	Seq    int    `json:"seq"`
	Ch     string `json:"ch"`
	At     Orig   `json:"at"`
	Level  *bool  `json:"level,omitempty"`
	Sample *int64 `json:"sample,omitempty"`
}

// ChAlias maps a raw channel name to a bus role and carries the original
// representation of the inversion flag.
type ChAlias struct {
	Raw     string `json:"raw"`
	Role    string `json:"role"`
	Invert  bool   `json:"invert"`
	OrigInv string `json:"orig_inv"`
}

// PolaritySrc records a clock polarity exactly as imported plus, when
// parseable, its normalized token.
type PolaritySrc struct {
	Role string `json:"role"`
	Orig string `json:"orig"`
	Pol  string `json:"pol,omitempty"`
	OK   bool   `json:"ok"`
}

// ProbeCfg is the probe/channel configuration block of an import document.
type ProbeCfg struct {
	Name        string        `json:"name"`
	SampleRate  Orig          `json:"sample_rate"`
	Aliases     []ChAlias     `json:"aliases"`
	Polarities  []PolaritySrc `json:"polarities"`
	Encoding    string        `json:"encoding,omitempty"`
	TimeBase    Orig          `json:"time_base,omitempty"`
	ValidWindow *Window       `json:"valid_window,omitempty"`
}

// Window is an explicit valid interval in seconds with its raw expressions.
type Window struct {
	Start Orig `json:"start"`
	End   Orig `json:"end"`
}

// Capture is immutable imported material. Changing source data appends a new
// revision (Amends) instead of mutating the previous capture.
type Capture struct {
	ID           string          `json:"id"`
	Label        string          `json:"label"`
	Kind         string          `json:"kind"`
	SourceFormat string          `json:"source_format"`
	ImportedAt   time.Time       `json:"imported_at"`
	SampleRate   Orig            `json:"sample_rate"`
	TimeBase     Orig            `json:"time_base,omitempty"`
	ValidWindow  *Window         `json:"valid_window,omitempty"`
	Aliases      []ChAlias       `json:"aliases"`
	Polarities   []PolaritySrc   `json:"polarities"`
	DecoderVer   string          `json:"decoder_version"`
	Edges        []Edge          `json:"edges"`
	Raw          json.RawMessage `json:"raw,omitempty"`
	Amends       string          `json:"amends,omitempty"`
	ChangeNote   string          `json:"change_note,omitempty"`
}

// Params is a versioned decoder parameter set.
type Params struct {
	ID           string    `json:"id"`
	CaptureID    string    `json:"capture_id"`
	Kind         string    `json:"kind"`
	ClockPol     string    `json:"clock_polarity"`
	SPIMode      string    `json:"spi_mode,omitempty"`
	SampleEdge   string    `json:"sample_edge,omitempty"`
	BitOrder     string    `json:"bit_order"`
	StretchTol   Orig      `json:"stretch_tol"`
	GapTol       Orig      `json:"gap_tol"`
	BoundaryTol  Orig      `json:"boundary_tol"`
	Note         string    `json:"note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Diagnostic points at a position (edge sequence, channel, byte offset) and
// explains why an input or result is not fully usable.
type Diagnostic struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"` // error|warning
	EdgeSeq  *int    `json:"edge_seq,omitempty"`
	Channel  string  `json:"channel,omitempty"`
	BytePos  *int    `json:"byte_pos,omitempty"`
	At       float64 `json:"at,omitempty"`
	Message  string  `json:"message"`
}

// BasisEvent is a machine-readable piece of evidence that justifies a
// candidate's existence, confidence or framing.
type BasisEvent struct {
	Code     string  `json:"code"`
	At       float64 `json:"at"`
	EdgeSeqs []int   `json:"edge_seqs"`
	Message  string  `json:"message"`
}

const (
	BasisMissingClockEdge = "MISSING_CLOCK_EDGE"
	BasisRepeatedStart    = "REPEATED_START"
	BasisClockStretch     = "CLOCK_STRETCH"
	BasisSPIModeAmbiguous = "SPI_MODE_AMBIGUOUS"
	BasisDataGap          = "DATA_GAP"
	BasisBoundarySample   = "BOUNDARY_SAMPLE"
	BasisMultiCS          = "MULTI_DEVICE_CS"
)

// ByteValue is one decoded byte. SlotID is a stable identity for the byte
// position derived from material identity + frame slot, independent of the
// value that different candidates put into it.
type ByteValue struct {
	SlotID   string   `json:"slot_id"`
	Order    int      `json:"order"`
	Value    int      `json:"value"`
	Hex      string   `json:"hex"`
	ACK      *bool    `json:"ack,omitempty"`
	ACKKnown bool     `json:"ack_known"`
	StartSec float64  `json:"start_sec"`
	EndSec   float64  `json:"end_sec"`
	EdgeSeqs []int    `json:"edge_seqs"`
	SampleAt []float64 `json:"sample_at"`
	Note     string   `json:"note,omitempty"`
}

// FrameCandidate is one legal decoding of a frame. Candidates sharing an
// AltGroup are mutually exclusive alternatives kept because the evidence does
// not uniquely decide between them.
type FrameCandidate struct {
	ID         string        `json:"id"`
	AltGroup   string        `json:"alt_group"`
	FrameSeq   int           `json:"frame_seq"`
	Protocol   string        `json:"protocol"`
	Device     string        `json:"device"`
	StartSec   float64       `json:"start_sec"`
	EndSec     float64       `json:"end_sec"`
	Bytes      []ByteValue   `json:"bytes"`
	Confidence float64       `json:"confidence"`
	Basis      []BasisEvent  `json:"basis"`
	Note       string        `json:"note,omitempty"`
	ValidFrom  float64       `json:"valid_from,omitempty"`
	ValidTo    float64       `json:"valid_to,omitempty"`
}

// DecodeResult is the derived layer for one capture under one parameter set.
type DecodeResult struct {
	ID          string           `json:"id"`
	CaptureID   string           `json:"capture_id"`
	ParamsID    string           `json:"params_id"`
	DecoderVer  string           `json:"decoder_version"`
	InputHash   string           `json:"input_hash"`
	ParentEvent string           `json:"parent_event"`
	Status      string           `json:"status"`
	Candidates  []FrameCandidate `json:"candidates"`
	Diagnostics []Diagnostic     `json:"diagnostics"`
	CreatedAt   time.Time        `json:"created_at"`
}

const (
	EventCaptureImport = "capture_import"
	EventCaptureRevise = "capture_revise"
	EventParamsSet     = "params_set"
	EventDecode        = "decode"
	EventDecision      = "decision"
	EventBranch        = "branch"
)

const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
	DecisionAdjust = "param_adjust"
)

// Event is one node of the append-only event tree. Walking ParentID recovers
// the exact dependency versions and visible conclusions at that point.
type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	ProjectID string          `json:"project_id"`
	ParentID  string          `json:"parent_id"`
	Branch    string          `json:"branch"`
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	CaptureID string          `json:"capture_id,omitempty"`
	ParamsID  string          `json:"params_id,omitempty"`
	ResultID  string          `json:"result_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// DecisionPayload is stored on decision events.
type DecisionPayload struct {
	Action     string  `json:"action"`
	TargetID   string  `json:"target_id"`
	AltGroup   string  `json:"alt_group"`
	Detail     string  `json:"detail,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
}

// BranchPayload records where a conflicting decision forked from.
type BranchPayload struct {
	BaseEventID      string `json:"base_event_id"`
	ConflictTargetID string `json:"conflict_target_id"`
	Reason           string `json:"reason"`
}

// Project groups an event tree and its derived views.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	HeadEvent string    `json:"head_event"`
	CreatedAt time.Time `json:"created_at"`
}

// View is the replayed visible state at one event.
type View struct {
	Event       Event                   `json:"event"`
	Captures    []Capture               `json:"captures"`
	Params      map[string]Params       `json:"params"`
	Results     map[string]DecodeResult `json:"results"`
	ParamsAt    map[string]string       `json:"params_at"`
	Current     map[string]string       `json:"current_capture"` // capture kind -> current capture id
	Decisions   map[string]DecisionPayload
	Branch      string                  `json:"branch"`
	Ancestors   []string                `json:"ancestor_ids"`
}

// ExportBundle is the portable project representation.
type ExportBundle struct {
	BundleType string    `json:"bundle_type"`
	Version    string    `json:"version"`
	ExportedAt time.Time `json:"exported_at"`
	Project    Project   `json:"project"`
	Events     []Event   `json:"events"`
	Captures   []Capture `json:"captures"`
	Params     []Params  `json:"params"`
	Results    []DecodeResult `json:"results"`
}

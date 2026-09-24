// Package domain holds the immutable source material (captures, channels,
// edges, original quantities) and the mutable decision layer (events,
// evidences, candidates, transactions). Nothing here performs I/O.
package domain

import "edgebench/internal/units"

// ---------------------------------------------------------------------------
// Immutable layer: once written these rows never change. A localized source
// correction appends a new edge set under a new capture version rather than
// mutating existing rows.
// ---------------------------------------------------------------------------

type Capture struct {
	ID            string         `json:"id"`
	ParentID      string         `json:"parentId,omitempty"`
	Version       int            `json:"version"`
	Label         string         `json:"label"`
	SampleRate    units.Quantity `json:"sampleRate"`
	TimeUnit      string         `json:"timeUnit"`
	OriginEventID string         `json:"originEventId"`
	InputDigest   string         `json:"inputDigest"`
}

type Channel struct {
	CaptureID string         `json:"captureId"`
	Alias     string         `json:"alias"`
	Physical  string         `json:"physical"`
	Role      string         `json:"role"`
	Inverted  bool           `json:"inverted"`
	Voltage   units.Quantity `json:"voltage"`
	Raw       map[string]any `json:"raw"`
}

// Edge is a single digital transition. TimePS is the normalized integer
// picosecond value; RawTime keeps the original textual timestamp and unit
// so the source representation is recoverable on every byte drill-down.
type Edge struct {
	ID        string         `json:"id"`
	CaptureID string         `json:"captureId"`
	Seq       int            `json:"seq"`
	Alias     string         `json:"alias"`
	Level     int            `json:"level"`
	TimePS    int64          `json:"timePs"`
	RawTime   string         `json:"rawTime"`
	RawUnit   string         `json:"rawUnit"`
	TimeValid bool           `json:"timeValid"`
	Raw       map[string]any `json:"raw,omitempty"`
}

// ---------------------------------------------------------------------------
// Mutable layer: an append-only event log. Derived rows reference the event
// (state version) and parent dependencies that produced them.
// ---------------------------------------------------------------------------

type EventKind string

const (
	EvImport        EventKind = "import"
	EvParamRevised  EventKind = "param_revised"
	EvSourcePatch   EventKind = "source_patch"
	EvDecode        EventKind = "decode"
	EvCandidateVote EventKind = "candidate_vote"
	EvReject        EventKind = "reject_transaction"
	EvConflict      EventKind = "decision_conflict"
	EvArchiveRun    EventKind = "archive_run"
)

type Event struct {
	Seq          int            `json:"seq"`
	ID           string         `json:"id"`
	ParentID     string         `json:"parentId,omitempty"`
	Kind         EventKind      `json:"kind"`
	CaptureID    string         `json:"captureId,omitempty"`
	BusID        string         `json:"busId,omitempty"`
	RulesVersion string         `json:"rulesVersion"`
	InputSummary string         `json:"inputSummary"`
	Detail       map[string]any `json:"detail,omitempty"`
	CreatedAtPS  int64          `json:"createdAtPs"`
}

// DecoderParams is a versioned parameter snapshot for one bus.
type DecoderParams struct {
	BusID          string `json:"busId"`
	Protocol       string `json:"protocol"` // i2c | spi | custom
	SCL            string `json:"scl,omitempty"`
	SDA            string `json:"sda,omitempty"`
	SCLK           string `json:"sclk,omitempty"`
	MOSI           string `json:"mosi,omitempty"`
	MISO           string `json:"miso,omitempty"`
	CS             string `json:"cs,omitempty"`
	CPOL           int    `json:"cpol"`
	CPHA           int    `json:"cpha"`
	SPIModeKnown   bool   `json:"spiModeKnown"`
	BitOrder       string `json:"bitOrder"`       // msb | lsb
	SampleBoundary string `json:"sampleBoundary"` // pre | post
	StretchHoldPS  int64  `json:"stretchHoldPs"`
	GapLimitPS     int64  `json:"gapLimitPs"`
	AddressBits    int    `json:"addressBits"`
	DecoderVersion string `json:"decoderVersion"`
}

// ---------------------------------------------------------------------------
// Derived layer
// ---------------------------------------------------------------------------

type EvidenceKind string

const (
	EvMissingClock  EvidenceKind = "missing_clock_edge"
	EvRepeatedStart EvidenceKind = "repeated_start"
	EvClockStretch  EvidenceKind = "clock_stretching"
	EvDataGap       EvidenceKind = "data_gap"
	EvMultiCS       EvidenceKind = "multi_device_chip_select"
	EvBoundary      EvidenceKind = "boundary_sample"
	EvNACK          EvidenceKind = "nack"
	EvAmbiguity     EvidenceKind = "ambiguous_spi_mode"
	EvTruncated     EvidenceKind = "truncated"
)

// Evidence is a machine-readable judgment about a structural condition.
// It records the policy consequence so replay does not rely on prose logs.
type Evidence struct {
	ID             string         `json:"id"`
	Kind           EvidenceKind   `json:"kind"`
	TransactionKey string         `json:"transactionKey,omitempty"`
	StartPS        int64          `json:"startPs"`
	EndPS          int64          `json:"endPs"`
	EdgeIDs        []string       `json:"edgeIds"`
	Message        string         `json:"message"`
	Policy         string         `json:"policy"` // multi_candidate | split_transaction | flag_only | ...
	Detail         map[string]any `json:"detail,omitempty"`
}

type Candidate struct {
	ID           string         `json:"id"`
	GroupID      string         `json:"groupId"`
	BusID        string         `json:"busId"`
	Mode         string         `json:"mode,omitempty"`
	Bytes        []DecodedByte  `json:"bytes"`
	Confidence   float64        `json:"confidence"`
	Adopted      bool           `json:"adopted"`
	Vetoed       bool           `json:"vetoed"`
	Rejected     bool           `json:"rejected"`
	Reason       string         `json:"reason,omitempty"`
	Evidences    []Evidence     `json:"evidences"`
	RulesVersion string         `json:"rulesVersion"`
	Detail       map[string]any `json:"detail,omitempty"`
}

type DecodedByte struct {
	Index    int      `json:"index"`
	Value    int      `json:"value"`
	Role     string   `json:"role"` // address | data | ...
	Ack      string   `json:"ack,omitempty"`
	StartPS  int64    `json:"startPs"`
	EndPS    int64    `json:"endPs"`
	EdgeIDs  []string `json:"edgeIds"`
	StableID string   `json:"stableId"`
}

type Transaction struct {
	Key          string      `json:"key"`
	BusID        string      `json:"busId"`
	Protocol     string      `json:"protocol"`
	StartPS      int64       `json:"startPs"`
	EndPS        int64       `json:"endPs"`
	Device       string      `json:"device,omitempty"`
	Candidates   []Candidate `json:"candidates"`
	Evidences    []Evidence  `json:"evidences"`
	RulesVersion string      `json:"rulesVersion"`
	Status       string      `json:"status"` // tentative | accepted | rejected
}

// DecodeReport is the deterministic output of a replay for one capture.
type DecodeReport struct {
	CaptureID    string             `json:"captureId"`
	Params       DecoderParams      `json:"params"`
	Transactions []Transaction      `json:"transactions"`
	Evidences    []Evidence         `json:"evidences"`
	Diagnostics  []units.Diagnostic `json:"diagnostics,omitempty"`
}

// ArchiveProbe is one single-source-change run of the counterexample
// archive. BaselineID == "" marks the baseline itself.
type ArchiveProbe struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Changed    string   `json:"changed"` // direct source that was changed
	ChangedID  string   `json:"changedId"`
	BaselineID string   `json:"baselineId,omitempty"`
	Affected   []string `json:"affected"`
	Unchanged  []string `json:"unchanged"`
	Contained  bool     `json:"contained"`
	Accepted   []string `json:"accepted"`
	Rejected   []string `json:"rejected"`
	Note       string   `json:"note"`
}

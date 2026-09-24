// Package model defines the immutable raw-material layer, the derived
// evidence layer and the mutable decision layer of the edge decoding bench.
//
// Layering rules:
//   - Raw layer (Import/Channel/Edge/ProbeConfig): values are preserved in
//     their original representation. Parsing and normalization only produce
//     additional fields; an un-convertible value stays in place with a
//     Diagnostic instead of being dropped or silently defaulted.
//   - Derived layer (Run/Candidate/Transaction/DecodedByte/BusEvent): every
//     record carries an input summary, the parameter version and parent
//     dependencies. Derived records are never mutated; a new run is appended.
//   - Decision layer (Decision/Event): reviewer actions append events and can
//     select/adjust candidates without rewriting historical conclusions.
package model

import "time"

// Kind values are stable strings so archives stay readable across versions.
const (
	KindProject    = "project"
	KindImport     = "import"
	KindChannel    = "channel"
	KindEdge       = "edge"
	KindProbe      = "probe_config"
	KindRun        = "decode_run"
	KindCandidate  = "candidate"
	KindTransaction = "transaction"
	KindByte       = "byte"
	KindBusEvent   = "bus_event"
	KindDiagnostic = "diagnostic"
	KindDecision   = "decision"
	KindEvent      = "event"
	KindExperiment = "experiment"
)

// Result kinds for the three *distinct* terminal failure classes. They must
// never be collapsed into one generic error by callers.
const (
	ResultOK             = "ok"
	ResultFormatCorrupt  = "format_corrupt"
	ResultBudgetExhausted = "budget_exhausted"
	ResultSemanticNoSolution = "semantic_no_solution"
)

// SourcePosition references a location inside an imported artifact.
type SourcePosition struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Offset int    `json:"offset"`
}

// Diagnostic carries a typed, positioned problem.
type Diagnostic struct {
	ID       string         `json:"id"`
	ProjectID string        `json:"project_id"`
	ImportID string         `json:"import_id,omitempty"`
	Code     string         `json:"code"`
	Severity string         `json:"severity"` // error | warning
	Message  string         `json:"message"`
	Position *SourcePosition `json:"position,omitempty"`
	Subject  string         `json:"subject,omitempty"` // stable id of affected object
	At       time.Time      `json:"at"`
}

// Quantity is a physical value kept in BOTH its original representation and a
// normalized SI-scaled integer form. Normalized==nil means the value could not
// be converted; Raw stays in place and a diagnostic explains why.
type Quantity struct {
	Raw        string `json:"raw"`
	Value      *int64 `json:"normalized,omitempty"`
	Unit       string `json:"unit"`            // normalized unit
	DisplayUnit string `json:"display_unit"`   // unit as written
	Convertible bool  `json:"convertible"`
}

// Channel is a named/aliased logical signal derived from one physical probe.
type Channel struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	ImportID  string `json:"import_id"`
	Index     int    `json:"index"`
	Alias     string `json:"alias"`
	Physical  string `json:"physical"`
	Inverted  bool   `json:"inverted"` // level inversion preserved as written
	Kind      string `json:"kind"`     // channel
}

// Edge is one observed transition. TimeRaw is the original token; TimePS is
// the normalized time in picoseconds (nil if un-convertible). LevelRaw is the
// written post-transition level token; LevelNormalized is derived after
// inversion handling (kept distinct from the raw level).
type Edge struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"project_id"`
	ImportID  string  `json:"import_id"`
	ChannelID string  `json:"channel_id"`
	Seq       int     `json:"seq"` // per-import, stable tie breaker
	TimeRaw   string  `json:"time_raw"`
	TimePS    *int64  `json:"time_ps,omitempty"`
	LevelRaw  string  `json:"level_raw"`
	// Level is the logical level used by decoders: 0/1, -1 = unknown.
	Level       int            `json:"level"`
	Inverted    bool           `json:"level_inverted_from_raw"`
	Position    SourcePosition `json:"position"`
	BudgetClass string         `json:"budget_class,omitempty"`
}

// SampleRate is recorded with its original numeric text and unit.
type SampleRate struct {
	Raw      string  `json:"raw"`
	Hertz    float64 `json:"hertz"`
	Unit     string  `json:"unit"`
}

// ProbeConfig is an immutable, versioned set of interpretation parameters.
type ProbeConfig struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Version   int    `json:"version"`
	Decoder   string `json:"decoder"`
	DecVer    string `json:"decoder_version"`
	Bus       string `json:"bus"` // i2c | spi | sync
	// channel aliases by role, e.g. SDA/SCL, MOSI/SCLK/CS
	Roles       map[string]string `json:"roles"`
	CPOL        *bool  `json:"clock_polarity,omitempty"` // nil => unknown (SPI ambiguity)
	CPHA        *bool  `json:"clock_phase,omitempty"`
	SampleRate  SampleRate        `json:"sample_rate"`
	// GapThresholdRaw/GapPS: idle gap that splits transactions.
	GapThresholdRaw string `json:"gap_threshold_raw"`
	GapPS           *int64 `json:"gap_ps,omitempty"`
	// StretchTolPS: min SCL-low duration that counts as clock stretching.
	StretchTolRaw string `json:"stretch_tol_raw"`
	StretchTolPS  *int64 `json:"stretch_tol_ps,omitempty"`
	SyncWord      string `json:"sync_word,omitempty"` // custom sync framing
	CreatedAt     time.Time `json:"created_at"`
	Note          string `json:"note,omitempty"`
}

// Import is one raw-material batch (immutable once accepted).
type Import struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Seq        int       `json:"seq"`
	FileName   string    `json:"fileName"`
	FormatVer  string    `json:"format_version"`
	RawSHA256  string    `json:"raw_sha256"`
	ByteCount  int       `json:"byte_count"`
	AcceptedAt time.Time `json:"accepted_at"`
}

// InputDigest summarizes exactly which inputs/parameters produced a run.
type InputDigest struct {
	ImportIDs   []string `json:"import_ids"`
	ImportSHAs  []string `json:"import_shas"`
	ProbeID     string   `json:"probe_id"`
	ProbeVer    int      `json:"probe_version"`
	DecVer      string   `json:"decoder_version"`
	RulesHash   string   `json:"rules_hash"`
}

// Run is one decoding execution over a fixed input digest.
type Run struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"project_id"`
	Seq         int         `json:"seq"`
	Bus         string      `json:"bus"`
	Input       InputDigest `json:"input"`
	Status      string      `json:"status"` // ok | partial | one of Result*
	Reason      string      `json:"reason,omitempty"`
	BudgetLimit int         `json:"budget_limit"`
	BudgetUsed  int         `json:"budget_used"`
	CreatedAt   time.Time   `json:"created_at"`
}

// Candidate is a legal interpretation of one frame/transaction. Multiple
// candidates are retained when the evidence does not uniquely determine one.
type Candidate struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	RunID         string  `json:"run_id"`
	TransactionID string  `json:"transaction_id"`
	Rank          int     `json:"rank"` // 1-based, deterministic
	Label         string  `json:"label"`
	Confidence    float64 `json:"confidence"`
	Tied          bool    `json:"tied_with_others"`
	Adopted       bool    `json:"adopted"` // adopted by a decision event
	Rejected       bool   `json:"rejected"`
	Reason        string  `json:"reason"`
}

// Transaction is a decoded (possibly fragmented) bus transaction.
type Transaction struct {
	ID         string  `json:"id"`
	ProjectID  string  `json:"project_id"`
	RunID      string  `json:"run_id"`
	CandID     string  `json:"candidate_id"`
	Index      int     `json:"index"`
	Bus        string  `json:"bus"`
	StartPS    int64   `json:"start_ps"`
	EndPS      int64   `json:"end_ps"`
	StartRaw   string  `json:"start_raw"`
	EndRaw     string  `json:"end_raw"`
	DeviceAddr string  `json:"device_addr,omitempty"`
	DeviceCS   string  `json:"device_cs,omitempty"`
	Fragmented bool    `json:"fragmented"`
	Complete   bool    `json:"complete"`
	StableKey  string  `json:"stable_key"` // same evidence => same key
}

// DecodedByte links one displayed byte value to the exact edges that formed it.
type DecodedByte struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	RunID         string  `json:"run_id"`
	CandID        string  `json:"candidate_id"`
	TransactionID string  `json:"transaction_id"`
	Offset        int     `json:"offset"`
	Value         *int    `json:"value"` // nil => indeterminate
	ValueText     string  `json:"value_text"`
	Role          string  `json:"role"` // address | data | sync | payload
	Ack           string  `json:"ack"`  // ack | nack | "" (SPI)
	StartPS       int64   `json:"start_ps"`
	EndPS         int64   `json:"end_ps"`
	StartRaw      string  `json:"start_raw"`
	EndRaw        string  `json:"end_raw"`
	EdgeIDs       []string `json:"edge_ids"`
	Partial       bool    `json:"partial"` // missing clock edges
	StableKey     string  `json:"stable_key"`
}

// BusEvent is a machine-readable structural event used as decision basis.
type BusEvent struct {
	ID        string   `json:"id"`
	ProjectID string   `json:"project_id"`
	RunID     string   `json:"run_id"`
	CandID    string   `json:"candidate_id"`
	Kind      string   `json:"kind"`
	AtPS      int64    `json:"at_ps"`
	AtRaw     string   `json:"at_raw"`
	Detail    string   `json:"detail"`
	EdgeIDs   []string `json:"edge_ids"`
	Subjects  []string `json:"subjects"`
}

// Standard bus-event kinds.
const (
	EvStart           = "start"
	EvRepeatedStart   = "repeated_start"
	EvStop            = "stop"
	EvAck             = "ack"
	EvNack            = "nack"
	EvClockStretch    = "clock_stretching"
	EvMissingClockEdge = "missing_clock_edge"
	EvDataGap         = "data_gap"
	EvBoundarySample  = "boundary_sample"
	EvMultiCS         = "multi_device_chip_select"
	EvTiedCandidates  = "tied_candidates"
	EvFragmentKept    = "fragment_kept"
	EvUnknownMode     = "spi_mode_unknown"
	EvBadValue        = "unconvertible_value"
	EvSyncFrame       = "sync_frame"
)

// Decision is an append-only reviewer act.
type Decision struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Seq       int       `json:"seq"`
	Kind      string    `json:"kind"` // accept | reject | adjust
	Subject   string    `json:"subject"`
	CandID    string    `json:"candidate_id,omitempty"`
	Params    string    `json:"params,omitempty"` // JSON adjustment
	Note      string    `json:"note,omitempty"`
	At        time.Time `json:"at"`
}

// Event is the append-only audit/history record (decision + parameter revisions
// + source changes + experiments). Old state is reconstructed by replay.
type Event struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Seq        int       `json:"seq"`
	Type       string    `json:"type"` // import | param_revision | source_change | decision | experiment | note
	Summary    string    `json:"summary"`
	Payload    string    `json:"payload,omitempty"` // JSON
	DepRunID   string    `json:"dep_run,omitempty"`
	DepProbeID string    `json:"dep_probe,omitempty"`
	At         time.Time `json:"at"`
}

// Experiment is one counterexample trial: baseline + exactly one changed source.
type Experiment struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	ArchiveID     string    `json:"archive_id"`
	Name          string    `json:"name"`
	ChangedSource string    `json:"changed_source"` // the single direct source
	ChangeKind    string    `json:"change_kind"`
	BaselineRunID string    `json:"baseline_run_id"`
	VariantRunID  string    `json:"variant_run_id"`
	Affected      []string  `json:"affected_objects"`
	Unchanged     []string  `json:"unchanged_objects"`
	Adopted       []string  `json:"adopted_candidate_ids"`
	Rejected      []string  `json:"rejected_candidate_ids"`
	BlastOK       bool      `json:"blast_radius_confined"`
	Note          string    `json:"note"`
	At            time.Time `json:"at"`
}

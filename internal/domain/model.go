// Package domain defines the layered evidence model of the edge decoding
// bench: immutable source material, versioned parameters, content-addressed
// derived decode results and mutable human decisions.
package domain

// DecoderVersion is the single business-rule version applied by every decode
// in this binary. It is recorded on every DecodeRun.
const DecoderVersion = "edgebench-decoder-1.0.0"

// EdgeLogFormat is the offline domain material format accepted on import.
const EdgeLogFormat = "edgelog/1"

// Kind values for Diagnostics.
const (
	DiagWarning = "warning" // material retained, interpretation limited
	DiagError   = "error"   // semantic problem; no fake success
)

// Class values for structured failures, kept mutually distinct.
const (
	ClassOK           = "ok"
	ClassFormatBroken = "format_broken"   // structurally corrupt material
	ClassSemanticVoid = "semantic_void"   // syntactically valid, no legal decode
	ClassBudgetExceed = "budget_exceeded" // resource budget exhausted
)

// Position locates a problem inside the imported material. All coordinates are
// carried explicitly; diagnostics never rely on implicit positions.
type Position struct {
	Filename string `json:"filename"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Token    string `json:"token,omitempty"`
	Context  string `json:"context,omitempty"`
}

// Diagnostics is a located problem with a stable code.
type Diagnostics struct {
	Code     string   `json:"code"`
	Kind     string   `json:"kind"` // warning | error
	Class    string   `json:"class"`
	Message  string   `json:"message"`
	Position Position `json:"position"`
}

// UnitQuantity keeps the original textual representation of a physical value
// beside its normalized SI value. When conversion is impossible Normalized is
// left nil and the value stays in place with a diagnostic instead of being
// dropped.
type UnitQuantity struct {
	Raw        string   `json:"raw"`
	Unit       string   `json:"unit,omitempty"`
	Normalized *float64 `json:"normalized,omitempty"` // SI (seconds / hertz)
}

// Channel is probe configuration for one digital channel: the raw alias,
// level inversion flag, declared initial level and original probe line.
type Channel struct {
	ID         string `json:"id"`          // stable identity, ch0001..
	Alias      string `json:"alias"`       // channel alias as imported
	Inverted   bool   `json:"inverted"`    // level inversion retained
	InitialRaw int    `json:"initial_raw"` // declared initial level, raw logic
	ProbeLine  int    `json:"probe_line"`  // line where the probe was declared
}

// Edge is one digital transition with the original time token preserved.
type Edge struct {
	ID         string       `json:"id"` // chID/line stable identity
	ChannelID  string       `json:"channel_id"`
	Line       int          `json:"line"`
	RawTime    UnitQuantity `json:"raw_time"`
	T          float64      `json:"t_sec"`     // normalized time, -1 when invalid
	RawLevel   int          `json:"raw_level"` // level after transition, raw logic
	Valid      bool         `json:"valid"`     // false when time cannot convert
	InFragment string       `json:"fragment_id,omitempty"`
}

// Fragment groups contiguous input lines (probes + one edge block) so a
// source patch only invalidates the dependent local fragment.
type Fragment struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	StartLine  int      `json:"start_line"`
	EndLine    int      `json:"end_line"`
	ChannelIDs []string `json:"channel_ids"`
	EdgeIDs    []string `json:"edge_ids"`
}

// Capture is the immutable imported material (layer 0).
type Capture struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Format      string        `json:"format"`
	RawText     string        `json:"-"` // retained blob
	RawHash     string        `json:"raw_hash"`
	SampleRate  UnitQuantity  `json:"sample_rate"` // samples per second + raw repr
	Timebase    UnitQuantity  `json:"timebase"`    // seconds per sample
	TimeZero    UnitQuantity  `json:"time_zero"`
	Channels    []Channel     `json:"channels"`
	Edges       []Edge        `json:"edges"`
	Fragments   []Fragment    `json:"fragments"`
	Diagnostics []Diagnostics `json:"diagnostics"`
	Class       string        `json:"class"`
	EdgeCount   int           `json:"edge_count"`
	ValidCount  int           `json:"valid_edge_count"`
}

// ChannelRef is one role binding inside a bus specification.
type ChannelRef struct {
	Role string `json:"role"` // scl|sda|clk|mosi|miso|cs0..|data
	ChID string `json:"channel_id"`
}

// BusSpec binds a decoder to channels and carries the retained clock
// polarity parameter (per bus).
type BusSpec struct {
	Name      string       `json:"name"` // I2C, SPI0, SPI1, SYNC ...
	Protocol  string       `json:"protocol"`
	Refs      []ChannelRef `json:"refs"`
	CPOL      string       `json:"cpol,omitempty"` // unknown|high|low (SPI idle level, effective)
	WordBits  int          `json:"word_bits,omitempty"`
	BitOrder  string       `json:"bit_order,omitempty"` // msb|lsb
	StretchNS float64      `json:"stretch_threshold_ns,omitempty"`
	GapNS     float64      `json:"data_gap_ns,omitempty"`
	Boundary  string       `json:"boundary_sampling,omitempty"` // right|strict
}

// ParamSet is a versioned decoder parameter layer.
type ParamSet struct {
	ID      string    `json:"id"`
	Version int       `json:"version"`
	Buses   []BusSpec `json:"buses"`
}

// Candidate is one legal parallel decode interpretation.
type Candidate struct {
	ID         string  `json:"id"`
	Value      string  `json:"value"` // hex byte e.g. 0x48
	Basis      string  `json:"basis"` // machine-readable reason code
	Confidence float64 `json:"confidence"`
	Ambiguous  bool    `json:"ambiguous"`
}

// Byte is one decoded byte/word with all legal candidates and provenance.
type Byte struct {
	ID         string      `json:"id"`
	Index      int         `json:"index"`
	Candidates []Candidate `json:"candidates"`
	ACKNACK    string      `json:"ack_nack,omitempty"` // ack|nack|none
	StartSec   float64     `json:"start_sec"`
	EndSec     float64     `json:"end_sec"`
	StartLine  int         `json:"start_line"`
	EndLine    int         `json:"end_line"`
	EdgeIDs    []string    `json:"edge_ids"` // every byte can return to edges
	SampleSecs []float64   `json:"sample_secs"`
	Valid      bool        `json:"valid"`
	Note       string      `json:"note,omitempty"`
}

// Transaction is one reassembled bus transaction.
type Transaction struct {
	ID          string   `json:"id"`
	Index       int      `json:"index"`
	Bus         string   `json:"bus"`
	StartSec    float64  `json:"start_sec"`
	EndSec      float64  `json:"end_sec"`
	StartLine   int      `json:"start_line"`
	EndLine     int      `json:"end_line"`
	Bytes       []Byte   `json:"bytes"`
	FragmentIDs []string `json:"fragment_ids"`
	Kind        string   `json:"kind"` // i2c|spi|sync
	Summary     string   `json:"summary"`
}

// RunJudgment is a machine-readable judgment event emitted by the decoder.
type RunJudgment struct {
	Code      string   `json:"code"` // e.g. missing-clock-edge, repeated-start ...
	Message   string   `json:"message"`
	Bus       string   `json:"bus"`
	StartSec  float64  `json:"start_sec"`
	EndSec    float64  `json:"end_sec"`
	StartLine int      `json:"start_line"`
	EdgeIDs   []string `json:"edge_ids"`
	ObjectIDs []string `json:"object_ids"` // affected byte/transaction IDs
	Adopted   bool     `json:"adopted"`    // adopted vs rejected interpretation
}

// BusEvent is a waveform-level protocol marker (START/STOP/stretch/...).
type BusEvent struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	Bus     string   `json:"bus"`
	T       float64  `json:"t_sec"`
	Line    int      `json:"line"`
	EdgeIDs []string `json:"edge_ids"`
	Note    string   `json:"note,omitempty"`
}

// DecodeRun is one content-addressed derivation (layer 2).
type DecodeRun struct {
	ID                 string        `json:"id"`
	CaptureID          string        `json:"capture_id"`
	ParamID            string        `json:"param_id"`
	DecoderVersion     string        `json:"decoder_version"`
	InputSummary       string        `json:"input_summary"`
	Buses              []string      `json:"buses"`
	Transactions       []Transaction `json:"transactions"`
	BusEvents          []BusEvent    `json:"bus_events"`
	Judgments          []RunJudgment `json:"judgments"`
	Diagnostics        []Diagnostics `json:"diagnostics"`
	Class              string        `json:"class"`
	DependentFragments []string      `json:"fragment_ids"`
}

// Decision is a mutable human judgment (layer 3).
type Decision struct {
	Kind        string `json:"kind"`        // accept|reject|param_adjust
	TargetType  string `json:"target_type"` // candidate|byte|transaction
	TargetID    string `json:"target_id"`
	CandidateID string `json:"candidate_id,omitempty"`
	Note        string `json:"note,omitempty"`
}

// Journal event kinds.
const (
	EvImport        = "import"
	EvParamRevise   = "param_revise"
	EvSourcePatch   = "source_patch"
	EvDecide        = "decide"
	EvBranchCreated = "branch_created"
)

// JournalEvent is one append-only, replayable event carrying its input
// summary, parameter version and parent dependency.
type JournalEvent struct {
	ID               string         `json:"id"`
	ProjectID        string         `json:"project_id"`
	Seq              int            `json:"seq"`
	ParentEventID    string         `json:"parent_event_id"`
	Kind             string         `json:"kind"`
	Label            string         `json:"label"`
	CaptureID        string         `json:"capture_id,omitempty"`
	ParamID          string         `json:"param_id,omitempty"`
	RunIDs           []string       `json:"run_ids,omitempty"`
	Decision         *Decision      `json:"decision,omitempty"`
	InputSummary     string         `json:"input_summary"`
	ParamVersion     int            `json:"param_version"`
	DecoderVersion   string         `json:"decoder_version"`
	ChangedFragments []string       `json:"changed_fragments,omitempty"`
	Branch           string         `json:"branch"`
	Payload          map[string]any `json:"payload,omitempty"`
}

// Branch is a named event chain.
type Branch struct {
	Name      string `json:"name"`
	HeadSeq   int    `json:"head_seq"`
	HeadEvent string `json:"head_event"`
	Parent    string `json:"parent_branch,omitempty"`
	ForkSeq   int    `json:"fork_seq,omitempty"`
}

// CounterExampleRecord is one archived counter-example check (adopted and
// rejected results both retained).
type CounterExampleRecord struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	Scenario      string        `json:"scenario"`
	DirectSource  string        `json:"direct_source"` // the single thing changed
	BaselineRunID string        `json:"baseline_run_id"`
	VariantRunID  string        `json:"variant_run_id"`
	Adopted       []string      `json:"adopted_object_ids"`
	Rejected      []string      `json:"rejected_object_ids"`
	Judgments     []RunJudgment `json:"judgments"`
	Scope         string        `json:"scope_verified"`
	CreatedSeq    int           `json:"created_seq"`
}

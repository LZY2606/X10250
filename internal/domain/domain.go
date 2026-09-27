// Package domain contains the immutable evidence model, decoder outputs and the
// mutable decision/event model of the edge decoding bench.
package domain

// Level is the logical level of a digital channel at an instant.
type Level int

const (
	LevelUnknown Level = -1
	LevelLow     Level = 0
	LevelHigh    Level = 1
)

// Channel describes a digital input channel using its source aliases.
type Channel struct {
	Name      string `json:"name"`
	Alias     string `json:"alias"`
	Inverted  bool   `json:"inverted"`
	InitLevel *int   `json:"init_level"` // source-probed idle level (before first edge)
}

// Edge is one source transition. Raw time representation is retained.
type Edge struct {
	ID       string `json:"id"`
	SourceSeq int   `json:"source_seq"`
	Channel  string `json:"channel"`
	Time     string `json:"time_raw"`
	TimeUnit string `json:"time_unit"`
	T        int64  `json:"t_ns"`
	TParsed  bool   `json:"time_parsed"`
	ToLevel  Level  `json:"to_level"`
}

// Probe holds probe/channel configuration exactly as imported.
type Probe struct {
	Channels   []Channel `json:"channels"`
	SampleRate string    `json:"sample_rate_raw"`
	RateUnit   string    `json:"sample_rate_unit"`
	RateHz     int64     `json:"sample_rate_hz,omitempty"`
	RateParsed bool      `json:"sample_rate_parsed"`
}

// Material is an immutable imported capture. LocalSource is preserved verbatim.
type Material struct {
	ID         string                 `json:"id"`
	Version    int                    `json:"version"`
	Supersedes string                 `json:"supersedes,omitempty"`
	Kind       string                 `json:"kind"` // capture
	ImportedAt string                 `json:"imported_at"`
	Probe      Probe                  `json:"probe"`
	Edges      []Edge                 `json:"edges"`
	LocalSource map[string]interface{} `json:"local_source"`
	Diagnostics []Diagnostic          `json:"diagnostics"`
}

// Params is a versioned decoder parameter set.
type Params struct {
	Version       int               `json:"version"`
	DecoderKind   string            `json:"decoder_kind"` // i2c | spi | custom
	DecoderVer    string            `json:"decoder_version"`
	ChannelRoles  map[string]string `json:"channel_roles"` // channel -> scl/sda/...
	ClockPolarity string            `json:"clock_polarity"` // rising|falling|unknown
	BitOrder      string            `json:"bit_order"`      // msb|lsb
	ActiveCSLevel string            `json:"active_cs_level"` // low|high
	WordBits      int               `json:"word_bits"`
	BoundaryMode  string            `json:"boundary_mode"` // simultaneous|sda_first
}

const (
	KindI2C    = "i2c"
	KindSPI    = "spi"
	KindCustom = "custom"
)

// EdgeRef points back to a specific source edge.
type EdgeRef struct {
	MaterialID string `json:"material_id"`
	EdgeID     string `json:"edge_id"`
	SourceSeq  int    `json:"source_seq"`
	T          int64  `json:"t_ns"`
}

// Byte is a decoded byte with per-bit evidence and a stable identity.
type Byte struct {
	ID          string    `json:"id"`
	Index       int       `json:"index"`
	Value       *int      `json:"value"` // nil when ambiguous
	ValueText   string    `json:"value_text"`
	AckNack     string    `json:"ack_nack"` // ack|nack|missing|none
	Bits        []BitRead `json:"bits"`
	StartT      int64     `json:"start_ns"`
	EndT        int64     `json:"end_ns"`
	CandidateOf string    `json:"candidate_of"`
	GapBefore   bool      `json:"gap_before"`
}

// BitRead links one sampled bit to the clock edge it was read on.
type BitRead struct {
	Position int     `json:"position"`
	Value    *int    `json:"value"` // nil => not uniquely determined (missing clock edge)
	ClockRef EdgeRef `json:"clock_ref"`
	DataRef  EdgeRef `json:"data_ref"`
}

// Candidate is one legal decoding of a transaction.
type Candidate struct {
	ID         string `json:"id"`
	Rank       int    `json:"rank"`
	Confidence float64 `json:"confidence"`
	Basis      string `json:"basis"`
	Bytes      []Byte `json:"bytes"`
	Unique     bool   `json:"unique"`
}

// Transaction is a decoded bus transaction (one START..STOP frame / one CS pulse).
type Transaction struct {
	ID         string     `json:"id"`
	Bus        string     `json:"bus"`
	Device     string     `json:"device,omitempty"`
	StartT     int64      `json:"start_ns"`
	EndT        int64      `json:"end_ns"`
	Candidates []Candidate `json:"candidates"`
	AcceptedID string     `json:"accepted_id,omitempty"`
	Rejected   bool       `json:"rejected"`
	Flags      []string   `json:"flags"` // repeated_start|stretch|gap|missing_clock|...
	OpenFrame  bool       `json:"open_frame"`
}

// Diagnostic is a positioned problem report.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error|warning
	Path     string `json:"path"`
	Message  string `json:"message"`
	T        int64  `json:"t_ns,omitempty"`
}

// Run statuses. Budget exhaustion, corruption and semantic undecidability are
// deliberately distinct outcomes.
const (
	StatusComplete        = "complete"
	StatusIncomplete      = "incomplete"
	StatusBudgetExhausted = "budget_exhausted"
	StatusCorrupt         = "corrupt"
	StatusUndecidable     = "undecidable"
)

// Run is an immutable decoder output.
type Run struct {
	ID           string                 `json:"id"`
	MaterialID   string                 `json:"material_id"`
	Params       Params                 `json:"params"`
	InputSummary map[string]interface{} `json:"input_summary"`
	Status       string                 `json:"status"`
	Transactions []Transaction          `json:"transactions"`
	Diagnostics  []Diagnostic           `json:"diagnostics"`
	DecoderVer   string                 `json:"decoder_version"`
}

// Event kinds for the mutable decision layer.
const (
	EvImport       = "material_imported"
	EvRun          = "decoder_run"
	EvAccept       = "candidate_accepted"
	EvReject       = "transaction_rejected"
	EvParamRevise  = "params_revised"
	EvSourcePatch  = "source_patched"
	EvConflict     = "decision_conflict"
	EvNote         = "review_note"
)

// Event is one append-only, replayable decision record.
type Event struct {
	Seq       int                    `json:"seq"`
	ID        string                 `json:"id"`
	Kind      string                 `json:"kind"`
	Branch    string                 `json:"branch"`
	ParentSeq int                    `json:"parent_seq"`
	At        string                 `json:"at"`
	Actor     string                 `json:"actor"`
	Summary   string                 `json:"summary"`
	Refs      map[string]string      `json:"refs"`
	Payload   map[string]interface{} `json:"payload"`
}

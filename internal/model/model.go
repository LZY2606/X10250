// Package model defines the immutable material layer, mutable decision layer
// and the event-sourced records of the edge decoding bench.
package model

// Pos is a position in imported source material, used in diagnostics.
type Pos struct {
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	Path string `json:"path,omitempty"` // e.g. "edges[12].t"
}

// Severity of a diagnostic.
type Severity string

const (
	SevError   Severity = "error"
	SevWarning Severity = "warning"
	SevInfo    Severity = "info"
)

// Diagnostic never uses empty/default values to fake success.
type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Pos      Pos      `json:"pos,omitempty"`
	Value    string   `json:"value,omitempty"` // raw representation preserved
}

// Qty is a physical quantity carrying its unit/coordinate reference.
// Raw preserves the imported representation; normalization happens in the
// derived layer only. When a value cannot be converted, Ok=false and the
// raw value stays in place with a diagnostic — it is never dropped.
type Qty struct {
	Raw   string  `json:"raw"`
	Unit  string  `json:"unit,omitempty"`  // physical unit, e.g. "ns", "Hz", "V"
	Basis string  `json:"basis,omitempty"` // reference basis, e.g. "sample-index"
	V     float64 `json:"v,omitempty"`
	Ok    bool    `json:"ok"`
}

// Edge is one raw digital transition.
type Edge struct {
	ID    string `json:"id"` // stable: anchor + ordinal
	T     Qty    `json:"t"`
	Level string `json:"level"` // "0" | "1" (logical level after this transition)
	Chan  string `json:"chan"`  // channel alias
}

// Probe describes a channel/probe configuration.
type Probe struct {
	Channel     string `json:"channel"`
	Alias       string `json:"alias"`
	InvertedRaw string `json:"inverted_raw"`
	Inverted    bool   `json:"inverted"`
	Role        string `json:"role"`                  // scl|sda|sck|mosi|miso|cs|sync|data
	ClkPolRaw   string `json:"clk_pol_raw,omitempty"` // clock polarity kept from material
	Volt        Qty    `json:"volt,omitempty"`
}

// Capture is the immutable material layer.
type Capture struct {
	FormatVersion string       `json:"format_version"`
	SourceFile    string       `json:"source_file"`
	CapturedAt    string       `json:"captured_at,omitempty"`
	SampleRate    Qty          `json:"sample_rate"`
	Probes        []Probe      `json:"probes"`
	Edges         []Edge       `json:"edges"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Params is a versioned, mutable decoder parameter set (derived from material).
type Params struct {
	Version          int     `json:"version"`
	I2CSampleFrac    float64 `json:"i2c_sample_frac"`    // sampling point within clock cell [0,1]
	I2CStretchNs     float64 `json:"i2c_stretch_ns"`     // clock stretching threshold
	GapNs            float64 `json:"gap_ns"`             // data gap threshold
	SPICpol          int     `json:"spi_cpol"`           // -1 unknown
	SPICpha          int     `json:"spi_cpha"`           // -1 unknown
	CustomActiveEdge string  `json:"custom_active_edge"` // "" unknown | rise | fall
	ClkPolarityRaw   string  `json:"clk_polarity_raw"`   // as inherited from material
}

// Byte is a decoded byte with a stable display identity.
type Byte struct {
	UID      string   `json:"uid"` // candidate scoped, deterministic
	Index    int      `json:"index"`
	Value    *int     `json:"value"` // nil when not uniquely decidable
	ValueHex string   `json:"value_hex"`
	Unknown  bool     `json:"unknown"`
	Kind     string   `json:"kind"`          // addr|data|reg|payload|partial
	Ack      string   `json:"ack,omitempty"` // ack|nack|none|cs_end|unknown
	Valid    Interval `json:"valid"`
	EdgeRefs []string `json:"edge_refs"` // back to raw edges
}

// Interval is a validity window in seconds, carrying its time basis.
type Interval struct {
	StartNs float64 `json:"start_ns"`
	EndNs   float64 `json:"end_ns"`
	Basis   string  `json:"basis"` // e.g. "normalized-from-raw-ns"
}

// Evidence links a decoder judgment to raw edge material.
type Evidence struct {
	UID    string   `json:"uid"`
	Kind   string   `json:"kind"` // start|stop|repeated_start|stretch|gap|missing_edge|boundary_sample|cs_assert|cs_deassert|sample|...
	AtNs   float64  `json:"at_ns"`
	Detail string   `json:"detail"`
	Edges  []string `json:"edge_refs"`
}

// Candidate is one legal decoding of a frame; ambiguity keeps all of them.
type Candidate struct {
	ID            string     `json:"id"` // stable, content-addressed
	GroupID       string     `json:"group_id"`
	Bus           string     `json:"bus"`  // i2c|spi|custom
	Mode          string     `json:"mode"` // e.g. "i2c", "spi-mode0".."spi-mode3", "custom-rise"
	FrameIdx      int        `json:"frame_idx"`
	Bytes         []Byte     `json:"bytes"`
	Evidence      []Evidence `json:"evidence"`
	Conf          float64    `json:"confidence"`
	Valid         Interval   `json:"valid"`
	Accepted      *bool      `json:"accepted,omitempty"` // decision (may be nil)
	Decision      string     `json:"decision,omitempty"` // accepted|rejected|pending
	RejectReason  string     `json:"reject_reason,omitempty"`
	Status        string     `json:"status"` // ok|ambiguous|invalid
	InvalidReason string     `json:"invalid_reason,omitempty"`
}

// Judgment is a machine-readable rule firing (the "judgment basis").
type Judgment struct {
	Rule     string   `json:"rule"`
	Outcome  string   `json:"outcome"`
	AtNs     float64  `json:"at_ns,omitempty"`
	Detail   string   `json:"detail"`
	EdgeRefs []string `json:"edge_refs,omitempty"`
}

// DecodeResult is the full derived layer for a capture+params combination.
type DecodeResult struct {
	BusHint       string       `json:"bus_hint,omitempty"`
	Candidates    []Candidate  `json:"candidates"`
	Judgments     []Judgment   `json:"judgments"`
	InputSummary  string       `json:"input_summary"`
	ParamsVersion int          `json:"params_version"`
	DecoderID     string       `json:"decoder_id"`
	Diagnostics   []Diagnostic `json:"diagnostics,omitempty"`
}

// Event kinds.
const (
	EvCaptureImported  = "CAPTURE_IMPORTED"
	EvDecoded          = "DECODED"
	EvParamRevised     = "PARAM_REVISED"
	EvSourceAmended    = "SOURCE_AMENDED"
	EvCandidateDecided = "CANDIDATE_DECIDED"
	EvCounterRun       = "COUNTER_RUN"
	EvProjectCreated   = "PROJECT_CREATED"
)

// Event is an append-only, immutable record.
type Event struct {
	Seq            int64         `json:"seq"`
	ID             string        `json:"id"`
	Branch         string        `json:"branch"`
	Kind           string        `json:"kind"`
	Actor          string        `json:"actor"`
	At             string        `json:"at"`
	Parents        []string      `json:"parents"`
	ParamsVersion  int           `json:"params_version"`
	CaptureAnchor  string        `json:"capture_anchor,omitempty"`
	CaptureVersion int           `json:"capture_version,omitempty"`
	Note           string        `json:"note,omitempty"`
	Capture        *Capture      `json:"capture,omitempty"`
	Params         *Params       `json:"params,omitempty"`
	Result         *DecodeResult `json:"result,omitempty"`
	Decision       *Decision     `json:"decision,omitempty"`
	Counter        *CounterRun   `json:"counter,omitempty"`
	Amendment      *Amendment    `json:"amendment,omitempty"`
}

// Amendment describes a localized source-data change and its blast radius.
type Amendment struct {
	TargetEdge   string   `json:"target_edge"`
	OldTRaw      string   `json:"old_t_raw"`
	NewTRaw      string   `json:"new_t_raw"`
	AffectedObjs []string `json:"affected_objects"`
	Reason       string   `json:"reason"`
}

// Decision is an accept/reject/adjust decision on a candidate.
type Decision struct {
	CandidateID string   `json:"candidate_id"`
	GroupID     string   `json:"group_id"`
	Verdict     string   `json:"verdict"` // accept|reject|adjust
	Reason      string   `json:"reason"`
	Conflicts   []string `json:"conflicts,omitempty"`
}

// CounterMutation is one single-source change in a counter run.
type CounterMutation struct {
	DirectSource string `json:"direct_source"` // clock_polarity|edge_time|...
	OldValue     string `json:"old_value"`
	NewValue     string `json:"new_value"`
}

// CounterRun is the machine-readable counter-example record.
type CounterRun struct {
	ID             string          `json:"id"`
	BaselineID     string          `json:"baseline_id"`
	Scenario       string          `json:"scenario"`
	Baseline       *Capture        `json:"baseline"`
	Mutation       CounterMutation `json:"mutation"`
	ExpectedScope  []string        `json:"expected_scope"`
	Affected       []string        `json:"affected"`
	ScopeOK        bool            `json:"scope_ok"`
	Adopted        []string        `json:"adopted"`
	NotAdopted     []NotAdopted    `json:"not_adopted"`
	BaselineResult *DecodeResult   `json:"baseline_result"`
	MutResult      *DecodeResult   `json:"mut_result"`
	Judgments      []Judgment      `json:"judgments"`
	Status         string          `json:"status"` // ok|limited|out-of-scope
	Note           string          `json:"note"`
}

// NotAdopted keeps a rejected/kept candidate with its reason.
type NotAdopted struct {
	CandidateID string `json:"candidate_id"`
	Reason      string `json:"reason"`
	Rule        string `json:"rule"`
}

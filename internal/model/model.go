// Package model holds the domain types shared by ingest, decoding, storage and
// the HTTP layer. Material (imported) data and mutable review decisions are
// distinct types; nothing here mutates an already computed result.
package model

import "time"

// Capture status values.
const (
	StatusOK          = "ok"
	StatusDiagnostics = "diagnostics"
)

// Diagnostic is a positioned message produced while importing or decoding.
type Diagnostic struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Value   string `json:"value,omitempty"`
}

// Edge is one imported digital transition. Raw time text and parse status are
// retained forever; Picoseconds is the normalized coordinate.
type Edge struct {
	CaptureUID  string `json:"capture_uid"`
	Seq         int    `json:"seq"`
	Channel     string `json:"channel"`
	RawTime     string `json:"raw_time"`
	Picoseconds int64  `json:"picoseconds"`
	TimeOK      bool   `json:"time_ok"`
	TimeReason  string `json:"time_reason,omitempty"`
	Level       *int   `json:"level,omitempty"`
	LevelOK     bool   `json:"level_ok"`
}

// ChannelDef describes a probe channel: alias, physical probe and inversion.
type ChannelDef struct {
	Alias  string `json:"alias"`
	Probe  string `json:"probe"`
	Role   string `json:"role"`
	Invert bool   `json:"invert"`
}

// Gap marks missing samples; decoders never stitch transactions across gaps.
type Gap struct {
	RawFrom string `json:"raw_from"`
	RawTo   string `json:"raw_to"`
	FromPs  int64  `json:"from_ps"`
	ToPs    int64  `json:"to_ps"`
	OK      bool   `json:"ok"`
	Reason  string `json:"reason,omitempty"`
	ParseReason string `json:"parse_reason,omitempty"`
}

// Capture is immutable imported material plus its provenance fields.
type Capture struct {
	UID            string       `json:"uid"`
	Label          string       `json:"label"`
	SampleRateRaw  string       `json:"sample_rate_raw"`
	SampleRateOK   bool         `json:"sample_rate_ok"`
	SampleRateReason string     `json:"sample_rate_reason,omitempty"`
	TimeBase       string       `json:"time_base"`
	SourceFormat   string       `json:"source_format"`
	SourceFile     string       `json:"source_file,omitempty"`
	ImportedAt     time.Time    `json:"imported_at"`
	RevisesUID     string       `json:"revises_uid,omitempty"`
	Status         string       `json:"status"` // ok | diagnostics
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
	Channels       []ChannelDef `json:"channels"`
	Edges          []Edge       `json:"edges"`
	Gaps           []Gap        `json:"gaps,omitempty"`
}

// I2CConfig / SPIConfig / CustomConfig are decoder parameter payloads.
type I2CConfig struct {
	SCL              string `json:"scl"`
	SDA              string `json:"sda"`
	SampleEdge       string `json:"sample_edge"`        // rising | falling
	BoundarySampling bool   `json:"boundary_sampling"`
	StretchMinRaw    string `json:"stretch_min_raw,omitempty"`
}

type SPIDevice struct {
	Alias      string `json:"alias"`
	DeviceID   string `json:"device_id"`
	ActiveHigh bool   `json:"active_high"`
}

type SPIConfig struct {
	SCLK             string      `json:"sclk"`
	MOSI             string      `json:"mosi,omitempty"`
	MISO             string      `json:"miso,omitempty"`
	Devices          []SPIDevice `json:"devices"`
	Mode             string      `json:"mode"` // unknown | 0 | 1 | 2 | 3
	WordBits         int         `json:"word_bits"`
	BitOrder         string      `json:"bit_order"` // msb | lsb
	BoundarySampling bool        `json:"boundary_sampling"`
}

type CustomConfig struct {
	Name       string   `json:"name"`
	Clock      string   `json:"clock"`
	Lines      []string `json:"lines"`
	Frame      string   `json:"frame,omitempty"`
	SampleEdge string   `json:"sample_edge"`
	WordBits   int      `json:"word_bits"`
	BitOrder   string   `json:"bit_order"`
}

// ParamVersion is an immutable revision of decoder parameters.
type ParamVersion struct {
	UID            string    `json:"uid"`
	ProjectUID     string    `json:"project_uid"`
	ParentUID      string    `json:"parent_uid,omitempty"`
	Bus            string    `json:"bus"` // i2c | spi | custom
	DecoderVersion string    `json:"decoder_version"`
	Note           string    `json:"note,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	I2C            *I2CConfig  `json:"i2c,omitempty"`
	SPI            *SPIConfig  `json:"spi,omitempty"`
	Custom         *CustomConfig `json:"custom,omitempty"`
}

// BitSample ties one decoded bit back to the clock edge it was sampled at.
type BitSample struct {
	Index    int    `json:"index"`
	EdgeSeq  int    `json:"edge_seq"` // clock edge sequence in the capture
	Value    *int   `json:"value"`    // nil when the clock edge is missing
	DataEdge int    `json:"data_edge_seq"`
	Boundary bool   `json:"boundary"`
}

// ByteCandidate is one legal value for a byte whose bits are not unique.
type ByteCandidate struct {
	Value      int          `json:"value"`
	Confidence float64      `json:"confidence"`
	Assumption string       `json:"assumption,omitempty"`
	Bits       []BitSample  `json:"bits"`
}

// DecodedByte is one displayed byte with stable identity and edge provenance.
type DecodedByte struct {
	UID        string         `json:"uid"`
	Index      int            `json:"index"`
	Line       string         `json:"line"`
	Kind       string         `json:"kind"` // address | data | ack | nack | unknown
	Candidates []ByteCandidate `json:"candidates"`
	StartEdge  int            `json:"start_edge"`
	EndEdge    int            `json:"end_edge"`
	Note       string         `json:"note,omitempty"`
}

// DecodedTransaction is one frame/transaction, or one alternative in a group.
type DecodedTransaction struct {
	UID        string          `json:"uid"`
	GroupUID   string          `json:"group_uid"`
	Bus        string          `json:"bus"`
	Device     string          `json:"device,omitempty"`
	Assumption string          `json:"assumption,omitempty"`
	Confidence float64        `json:"confidence"`
	StartEdge  int             `json:"start_edge"`
	EndEdge    int             `json:"end_edge"`
	State      string          `json:"state"` // complete | truncated | open
	Flags      []string        `json:"flags"`
	Bytes      []*DecodedByte  `json:"bytes"`
}

// DecodeEvent is a machine-readable piece of structured evidence.
type DecodeEvent struct {
	Code      string   `json:"code"`
	AtPs      *int64   `json:"at_ps,omitempty"`
	EdgeSeqs  []int    `json:"edge_seqs,omitempty"`
	TrxUIDs   []string `json:"trx_uids,omitempty"`
	ByteUIDs  []string `json:"byte_uids,omitempty"`
	Severity  string   `json:"severity"`
	Message   string   `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
}

// InputSummary records what a computation consumed.
type InputSummary struct {
	CaptureUID     string `json:"capture_uid"`
	CaptureHash    string `json:"capture_hash"`
	ParamUID       string `json:"param_uid"`
	ParamHash      string `json:"param_hash"`
	DecoderVersion string `json:"decoder_version"`
}

// DecodeResult is the full, immutable output of one decoder run.
type DecodeResult struct {
	Bus          string               `json:"bus"`
	Status       string               `json:"status"` // ok | semantic_no_solution
	Input        InputSummary         `json:"input"`
	Transactions []*DecodedTransaction `json:"transactions"`
	Events       []DecodeEvent        `json:"events"`
	Diagnostics  []Diagnostic         `json:"diagnostics,omitempty"`
}

// Run persists one computation plus its parent dependency.
type Run struct {
	UID        string    `json:"uid"`
	ProjectUID string    `json:"project_uid"`
	CaptureUID string    `json:"capture_uid"`
	ParamUID   string    `json:"param_uid"`
	ParentRun  string    `json:"parent_run,omitempty"`
	DecoderVersion string `json:"decoder_version"`
	CreatedAt  time.Time `json:"created_at"`
	Result     *DecodeResult `json:"result,omitempty"`
	Status     string    `json:"status"` // ok | semantic_no_solution | budget_exhausted
	Detail     string    `json:"detail,omitempty"`
}

// Decision is an append-only review decision.
type Decision struct {
	UID        string    `json:"uid"`
	ProjectUID string    `json:"project_uid"`
	EventSeq   int       `json:"event_seq"`
	TargetType string    `json:"target_type"` // transaction_group | byte | param
	TargetUID  string    `json:"target_uid"`
	Verdict    string    `json:"verdict"` // accepted | rejected | adjusted
	NewParamUID string   `json:"new_param_uid,omitempty"`
	Note       string    `json:"note,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// Event is the append-only project history.
type Event struct {
	Seq        int       `json:"seq"`
	ProjectUID string    `json:"project_uid"`
	Kind       string    `json:"kind"`
	At         time.Time `json:"at"`
	Payload    string    `json:"payload"`
	RunUID     string    `json:"run_uid,omitempty"`
}

// Project groups material, parameters, runs and decisions.
type Project struct {
	UID       string    `json:"uid"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CaseMutation keeps one single-source counterexample mutation.
type CaseMutation struct {
	Name           string   `json:"name"`
	ChangedSource  string   `json:"changed_source"` // clock_polarity | boundary_sampling | repeated_start | clock_stretching | data_gap | multi_device_cs | tied_candidates
	InputFragment  string   `json:"input_fragment"`
	Status         string   `json:"status"`
	AffectedObjects []string `json:"affected_objects"`
	Adopted        *AdoptedResult `json:"adopted,omitempty"`
	Rejected       []AdoptedResult `json:"rejected,omitempty"`
	Evidence       []DecodeEvent   `json:"evidence"`
}

// AdoptedResult records a candidate and whether the archive took it.
type AdoptedResult struct {
	TrxUID     string  `json:"trx_uid"`
	Assumption string  `json:"assumption,omitempty"`
	ByteUID    string  `json:"byte_uid,omitempty"`
	Value      string  `json:"value,omitempty"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
}

// Case is the counterexample archive entry.
type Case struct {
	UID        string                 `json:"uid"`
	ProjectUID string                 `json:"project_uid"`
	Label      string                 `json:"label"`
	CreatedAt  time.Time                `json:"created_at"`
	Baseline   map[string]any          `json:"baseline"`
	Mutations  []CaseMutation          `json:"mutations"`
}

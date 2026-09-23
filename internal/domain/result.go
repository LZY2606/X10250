package domain

// FindingCode enumerates the structured states the decoders emit. These are
// machine-readable evidence, not free-text logs.
type FindingCode string

const (
	FindingClockStretch      FindingCode = "clock_stretch"
	FindingRepeatedSTART     FindingCode = "repeated_start"
	FindingMissingClockEdge  FindingCode = "missing_clock_edge"
	FindingDataGap           FindingCode = "data_gap"
	FindingSPIModeAmbiguous  FindingCode = "spi_mode_ambiguous"
	FindingBoundarySampling  FindingCode = "boundary_sampling"
	FindingClockPolarity     FindingCode = "clock_polarity"
	FindingMultiDeviceCS     FindingCode = "multi_device_cs"
	FindingPartialByte       FindingCode = "partial_byte"
	FindingNACK              FindingCode = "nack"
	FindingUnconvertibleTime FindingCode = "unconvertible_time"
	FindingInvalidLevel      FindingCode = "invalid_level"
	FindingNoStart           FindingCode = "no_start_condition"
)

// Severity levels.
type Severity string

const (
	SevInfo    Severity = "info"
	SevWarning Severity = "warning"
	SevError   Severity = "error"
)

// Finding is a structured state attached to a transaction/candidate/byte.
type Finding struct {
	Code        FindingCode `json:"code"`
	Severity    Severity    `json:"severity"`
	Message     string      `json:"message"`
	StartTPS    int64       `json:"start_tps"`
	EndTPS      int64       `json:"end_tps"`
	EdgeRefs    []string    `json:"edge_refs"`
	ByteRefs    []string    `json:"byte_refs"`
	DiagnosticID string     `json:"diagnostic_id,omitempty"`
}

// ByteRef is one decoded byte with full provenance back to edges.
type ByteRef struct {
	ID           string   `json:"id"`
	Index        int      `json:"index"`
	Value        *int     `json:"value"` // nil when the value is not uniquely decidable
	ValueText    string   `json:"value_text"`
	AckNack      string   `json:"ack_nack"` // ack | nack | "" (SPI/sync)
	StartTPS     int64    `json:"start_tps"`
	EndTPS       int64    `json:"end_tps"`
	EdgeRefs     []string `json:"edge_refs"`
	ClockEdgeRefs []string `json:"clock_edge_refs"`
	Confidence   float64  `json:"confidence"`
	Unique       bool     `json:"unique"`
	Findings     []Finding `json:"findings"`
}

// Transaction is one reassembled bus transaction. Data gaps never glue two
// segments into one transaction.
type Transaction struct {
	ID         string    `json:"id"`
	Index      int       `json:"index"`
	Kind       string    `json:"kind"` // i2c | spi | sync
	Device     string    `json:"device"` // CS alias for SPI; addr for I2C
	StartTPS   int64     `json:"start_tps"`
	EndTPS     int64     `json:"end_tps"`
	Bytes      []ByteRef `json:"bytes"`
	EdgeRefs   []string  `json:"edge_refs"`
	Confidence float64   `json:"confidence"`
	Valid      bool      `json:"valid"`
	Findings   []Finding `json:"findings"`
}

// Candidate is one legal decoding under a parameter hypothesis (e.g. SPI mode
// 0..3 when mode is unknown). Equivalent inputs produce a stable candidate
// ordering keyed by Hypothesis.
type Candidate struct {
	ID           string        `json:"id"`
	Hypothesis   string        `json:"hypothesis"`
	HypothesisLabel string     `json:"hypothesis_label"`
	Rank         int           `json:"rank"`
	Confidence   float64       `json:"confidence"`
	Preferred    bool          `json:"preferred"`
	Transactions []Transaction `json:"transactions"`
	Findings     []Finding     `json:"findings"`
}

// InputSummary is the digest of what a compute step consumed.
type InputSummary struct {
	CaptureID  string `json:"capture_id"`
	EdgeCount  int    `json:"edge_count"`
	EdgeHash   string `json:"edge_hash"`
	ChannelHash string `json:"channel_hash"`
	ProbeHash  string `json:"probe_hash"`
	TimeBasePS int64  `json:"time_base_ps"` // always 1ps
	SampleRateHz int64 `json:"sample_rate_hz"`
}

// DecodeResult is an immutable compute output. It records parameter version
// and parent dependencies; recomputation never overwrites an old result.
type DecodeResult struct {
	ID           string       `json:"id"`
	CaptureID    string       `json:"capture_id"`
	ParamsID     string       `json:"params_id"`
	ParentResultID string     `json:"parent_result_id"`
	BusKind      string       `json:"bus_kind"`
	DecoderVersion string     `json:"decoder_version"`
	Input        InputSummary `json:"input"`
	Candidates   []Candidate  `json:"candidates"`
	Diagnostics  []Diagnostic `json:"diagnostics"`
	Status       string       `json:"status"` // ok | partial
	ValidFromTPS int64        `json:"valid_from_tps"`
	ValidToTPS   int64        `json:"valid_to_tps"`
}

// Diagnostic is a located error/notice. Incomplete input yields diagnostics
// with positions rather than empty/default success values.
type Diagnostic struct {
	ID       string    `json:"id"`
	Code     string    `json:"code"`
	Severity Severity  `json:"severity"`
	Message  string    `json:"message"`
	Position Position  `json:"position"`
}

package model

// Structured bus event codes. These are machine readable judgments, not log
// lines: the review UI and replay verification consume them directly.
const (
	EvStart             = "START"
	EvRepeatedStart     = "REPEATED_START"
	EvStop              = "STOP"
	EvAck               = "ACK"
	EvNack              = "NACK"
	EvClockStretch      = "CLOCK_STRETCHING"
	EvMissingClockEdge  = "CLOCK_MISSING_EDGE"
	EvBoundarySample    = "BOUNDARY_SAMPLE"
	EvDataGap           = "DATA_GAP"
	EvMultiChipSelect   = "MULTI_DEVICE_CHIP_SELECT"
	EvCSOverlap         = "CHIP_SELECT_OVERLAP"
	EvClockPolarity     = "CLOCK_POLARITY_MISMATCH"
	EvAmbiguousMode     = "SPI_MODE_AMBIGUOUS"
	EvCandidateParallel = "PARALLEL_CANDIDATES"
	EvIncompleteByte    = "INCOMPLETE_BYTE"
	EvOrphanToggle      = "ORPHAN_CLOCK_TOGGLE"
	EvSyncAlign         = "SYNC_ALIGN"
	EvSyncAmbiguous     = "SYNC_ALIGN_AMBIGUOUS"
	EvUnconvertible     = "UNCONVERTIBLE_VALUE"
)

// AckLevel is the per-byte acknowledge status.
type AckLevel string

const (
	AckAck     AckLevel = "ACK"
	AckNack    AckLevel = "NACK"
	AckUnknown AckLevel = "UNKNOWN"
	AckNone    AckLevel = "NONE"
)

// Byte is one decoded byte with full provenance back to edge sequence ids.
type Byte struct {
	// ID is a stable identity: capture/protocol/frame-ordinal/byte-ordinal
	// within a candidate. It is identical for equivalent inputs and survives
	// export/import.
	ID       string `json:"id"`
	Ordinal  int    `json:"ordinal"`
	Value    int    `json:"value"`
	ValueHex string `json:"value_hex"`
	// Role labels the byte position, e.g. ADDRESS, DATA, MOSI, MISO.
	Role string   `json:"role"`
	Ack  AckLevel `json:"ack"`
	// EdgeRefs are seq numbers of the clock/data edges that produced it.
	EdgeRefs     []int   `json:"edge_refs"`
	StartSample  int64   `json:"start_sample"`
	EndSample    int64   `json:"end_sample"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	// Unit is always recorded for displayed quantities.
	TimeUnit string `json:"time_unit"`
	Partial  bool   `json:"partial,omitempty"`
}

// Frame is one decoded transaction / chip-select frame.
type Frame struct {
	ID           string  `json:"id"`
	Protocol     string  `json:"protocol"`
	Ordinal      int     `json:"ordinal"`
	Device       string  `json:"device,omitempty"`
	Bytes        []Byte  `json:"bytes"`
	StartSample  int64   `json:"start_sample"`
	EndSample    int64   `json:"end_sample"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	TimeUnit     string  `json:"time_unit"`
	// Complete is false when missing clock edges or a gap cut the frame off.
	Complete bool `json:"complete"`
	// JoinedAcrossGap is always false by rule; the field exists so consumers
	// can assert the invariant explicitly.
	JoinedAcrossGap bool `json:"joined_across_gap"`
}

// BusEvent is a structured judgment tied to samples and edges.
type BusEvent struct {
	Code         string  `json:"code"`
	Severity     string  `json:"severity"`
	Message      string  `json:"message"`
	Channel      string  `json:"channel,omitempty"`
	StartSample  int64   `json:"start_sample"`
	EndSample    int64   `json:"end_sample"`
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
	TimeUnit     string  `json:"time_unit"`
	EdgeRefs     []int   `json:"edge_refs,omitempty"`
	FrameID      string  `json:"frame_id,omitempty"`
	ByteID       string  `json:"byte_id,omitempty"`
	// CandidateKey links the event to one parallel candidate when relevant.
	CandidateKey string `json:"candidate_key,omitempty"`
}

// Candidate is one legal decoding under uncertain clock polarity/mode/sync.
type Candidate struct {
	Key        string  `json:"key"`
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	// TiedWith lists other candidate keys sharing the same confidence.
	TiedWith []string   `json:"tied_with,omitempty"`
	Frames   []Frame    `json:"frames"`
	Events   []BusEvent `json:"events"`
	// Basis are machine readable rule citations for this candidate.
	Basis []string `json:"basis"`
}

// Result is everything a decoder version derives from one capture/params pair.
type Result struct {
	CaptureID   string          `json:"capture_id"`
	CaptureRev  int             `json:"capture_revision"`
	ParamsRev   int             `json:"params_revision"`
	Decoder     string          `json:"decoder_version"`
	Candidates  []Candidate     `json:"candidates"`
	Diagnostics []Diagnostic    `json:"diagnostics"`
	Intervals   []ValidInterval `json:"valid_intervals"`
	// InputSummary is the digest of the normalised edges + params used.
	InputSummary string `json:"input_summary"`
}

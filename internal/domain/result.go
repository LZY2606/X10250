package domain

// EvidenceKind tags machine-readable evidence events. Review screens and
// external tools consume these rather than grepping human logs.
type EvidenceKind string

const (
	EvClockMissingEdge EvidenceKind = "clock_missing_edge"
	EvRepeatedStart    EvidenceKind = "repeated_start"
	EvClockStretching  EvidenceKind = "clock_stretching"
	EvSpiModeUnknown   EvidenceKind = "spi_mode_unknown"
	EvDataGap          EvidenceKind = "data_gap"
	EvBoundarySample   EvidenceKind = "boundary_sample"
	EvMultiDeviceCS    EvidenceKind = "multi_device_cs"
	EvParallelCands    EvidenceKind = "parallel_candidates"
	EvAmbiguousKept    EvidenceKind = "ambiguous_kept"
	EvNoJoinAcrossGap  EvidenceKind = "no_join_across_gap"
)

// Evidence is a structured, replayable judgment record. Each record names
// the rule version, the input fragment and the affected objects.
type Evidence struct {
	ID         string        `json:"id"`
	Kind       EvidenceKind  `json:"kind"`
	BusID      string        `json:"busId"`
	RuleVer    string        `json:"ruleVersion"`
	InputDigest string       `json:"inputDigest"`
	StartNS    int64         `json:"startNs"`
	EndNS      int64         `json:"endNs"`
	Detail     map[string]string `json:"detail,omitempty"`
	Affects    []string      `json:"affects"`
}

// Bit anchors one decoded bit to its clock/data source edges.
type Bit struct {
	Index     int    `json:"index"`
	Value     int    `json:"value"`
	SampleNS  int64  `json:"sampleNs"`
	ClockEdge string `json:"clockEdge"` // edge ID used as sample strobe
	DataEdge  string `json:"dataEdge,omitempty"`
	Ambiguous bool  `json:"ambig,omitempty"`
}

// Byte is one decoded byte. Bits point back to individual edges so the UI
// can walk byte -> bit -> edge. Valid is false when the frame is partial.
type Byte struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Value    *int   `json:"value"`
	Hex      string `json:"hex"`
	StartNS  int64  `json:"startNs"`
	EndNS    int64  `json:"endNs"`
	Bits     []Bit  `json:"bits"`
	Ack      string `json:"ack,omitempty"` // ack | nak | none
	AckNS    *int64 `json:"ackNs,omitempty"`
	Valid    bool   `json:"valid"`
	Reason   string `json:"reason,omitempty"`
}

// Transaction is one reassembled frame. For ambiguous input several
// candidates share one GroupID; they carry the same source span and the
// UI presents them as parallel readings.
type Transaction struct {
	ID        string            `json:"id"`
	GroupID   string            `json:"groupId"`
	BusID     string            `json:"busId"`
	Kind      string            `json:"kind"` // i2c | spi | sync
	StartNS   int64             `json:"startNs"`
	EndNS     int64             `json:"endNs"`
	Bytes     []*Byte           `json:"bytes"`
	Summary   string            `json:"summary"`
	Confidence float64          `json:"confidence"`
	Ambiguous bool              `json:"ambig"`
	Valid     bool              `json:"valid"`
	Reason    string            `json:"reason,omitempty"`
	Attrs     map[string]string `json:"attrs,omitempty"`
}

// Candidate is one legal reading of an ambiguous transaction group.
type Candidate struct {
	ID         string       `json:"id"`
	GroupID    string       `json:"groupId"`
	BusID      string       `json:"busId"`
	Label      string       `json:"label"`
	Txn        *Transaction `json:"txn"`
	Confidence float64      `json:"confidence"`
	Chosen     bool         `json:"chosen,omitempty"`
}

// BusResult is the complete output for one declared bus.
type BusResult struct {
	BusID       string        `json:"busId"`
	Protocol    string        `json:"protocol"`
	Decoded     bool          `json:"decoded"`
	Diags       []*Diagnostic `json:"diags"`
	Txns        []*Transaction `json:"txns"`
	Candidates  []*Candidate   `json:"candidates"`
	Evidence    []*Evidence    `json:"evidence"`
	InputDigest string         `json:"inputDigest"`
	ParamsHash  string         `json:"paramsHash"`
	RuleVersion string         `json:"ruleVersion"`
}

// Result is the derived layer for a concrete event prefix: input digest,
// parameter version and parent dependency all recorded per result.
type Result struct {
	Version       int                      `json:"version"`
	AtEventSeq    int                      `json:"atEventSeq"`
	Buses         map[string]*BusResult    `json:"buses"`
	Order         []string                 `json:"order"`
	Diags         []*Diagnostic            `json:"diags"`
	AllEvidence   []*Evidence              `json:"evidence"`
	InputDigest   string                   `json:"inputDigest"`
	ParamsVersion string                   `json:"paramsVersion"`
	ParentDigest  string                   `json:"parentDigest,omitempty"`
}

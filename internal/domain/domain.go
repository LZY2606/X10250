// Package domain defines the immutable raw materials, versioned parameters,
// derived decode results and append-only decision/event model for the edge
// decoding bench.
//
// Layering rule (see docs):
//
//	Capture/Channel/Edge ............ immutable imported material
//	ParamSet/ParamVersion ........... versioned, auditable decoder settings
//	Run/Candidate/Transaction/Byte .. derived layer, each naming inputs+parents
//	Decision/Event .................. reviewer intent and the append-only log
//
// All domain quantities carry their original textual representation plus a
// normalized value and unit. A value that cannot be converted is kept at its
// original position with a Diagnostic instead of being dropped.
package domain

import (
	"fmt"
	"sort"
	"strings"
)

// ResultClass separates the three failure outcomes that must never be
// conflated: a hard resource limit (BudgetExhausted), unreadable/corrupt input
// (FormatCorrupt) and readable input that admits no legal interpretation
// (SemanticUnsolvable).
type ResultClass string

const (
	ClassOK                 ResultClass = "ok"
	ClassBudgetExhausted    ResultClass = "budget_exhausted"
	ClassFormatCorrupt      ResultClass = "format_corrupt"
	ClassSemanticUnsolvable ResultClass = "semantic_unsolvable"
)

// Severity ranks diagnostics. Fatal diagnostics turn a parse into a
// FormatCorrupt result; error diagnostics keep the value in place and the
// capture importable; warnings never hide data.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
	SeverityFatal   Severity = "fatal"
)

// Diagnostic is always positional: it names the capture, the path inside the
// payload and an offset/window so a reviewer can jump back to the offending
// edge. Zero values in Position fields mean "not applicable".
type Diagnostic struct {
	Code      string   `json:"code"`
	Severity  Severity `json:"severity"`
	Message   string   `json:"message"`
	Position  Position `json:"position"`
	Original  string   `json:"original,omitempty"`
	KeptAtPos bool     `json:"kept_at_position,omitempty"`
}

// Position addresses material in an import or in a capture.
type Position struct {
	CaptureID string `json:"capture_id,omitempty"`
	JSONPath  string `json:"json_path,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	EdgeIndex int    `json:"edge_index,omitempty"`
	// EdgeID is populated once channel edges have stable identities.
	EdgeID string `json:"edge_id,omitempty"`
	// TimeNS is the normalized time nearest to the position when known.
	TimeNS float64 `json:"time_ns,omitempty"`
	// SpanNS gives a window [TimeNS, TimeNS+SpanNS] when the diagnostic covers
	// a region (a data gap, a stretch interval...).
	SpanNS float64 `json:"span_ns,omitempty"`
}

func (p Position) String() string {
	parts := []string{}
	if p.JSONPath != "" {
		parts = append(parts, p.JSONPath)
	}
	if p.ChannelID != "" {
		parts = append(parts, "ch="+p.ChannelID)
	}
	if p.EdgeID != "" {
		parts = append(parts, "edge="+p.EdgeID)
	} else if p.EdgeIndex >= 0 {
		parts = append(parts, fmt.Sprintf("edge#%d", p.EdgeIndex))
	}
	if p.TimeNS != 0 || p.SpanNS != 0 {
		parts = append(parts, fmt.Sprintf("t=%.3fns+%.3fns", p.TimeNS, p.SpanNS))
	}
	return strings.Join(parts, " ")
}

// Quantity preserves the original token, the parsed normalized value and its
// unit. Unit is empty for dimensionless quantities.
type Quantity struct {
	Original string  `json:"original"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	// Unconvertible marks tokens kept verbatim because they cannot be
	// normalized. Value is 0 and the diagnostic is attached to the owner.
	Unconvertible bool `json:"unconvertible,omitempty"`
}

// ProbeConfig is the immutable per-capture probe/channel bookkeeping.
type ProbeConfig struct {
	// TimeBase is the capture time-base label (e.g. "t0=trigger").
	TimeBase string `json:"time_base"`
	// SampleRate holds the original sample-rate token (e.g. "5 MS/s").
	SampleRate Quantity `json:"sample_rate"`
	// TimeUnit is the unit edges are expressed in on import ("ns","us","ms").
	TimeUnit string `json:"time_unit"`
	// TimeBaseNS stores the origin offset in nanoseconds when present.
	TimeBaseNS float64 `json:"time_base_ns,omitempty"`
	// DecoderVersion is the exact decoder rule set that produced the stored
	// edges ("edgebench-i2c@1.0.0", ...). Raw material retains it forever.
	DecoderVersion string `json:"decoder_version"`
}

// Channel is an immutable digital channel description.
type Channel struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
	Role  string `json:"role,omitempty"`
	// Inverted records the probe-level level inversion. Edge levels are
	// normalized with inversion applied; the flag itself never disappears.
	Inverted bool `json:"inverted"`
}

// EdgeInput is the verbatim edge token from the import payload.
type EdgeInput struct {
	Time Quantity `json:"time"`
	// Level is the post-inversion logical level ("0"/"1") when provided.
	Level string `json:"level"`
	// RawLevel is the level as written before inversion normalization.
	RawLevel string `json:"raw_level"`
}

// Edge is the normalized immutable edge. TimeNS is nanoseconds relative to
// ProbeConfig.TimeBaseNS. Identities are content-derived and stable across
// export/delete/reimport.
type Edge struct {
	ID      string  `json:"id"`
	Index   int     `json:"index"`
	TimeNS  float64 `json:"time_ns"`
	Level   int     `json:"level"`
	OrigTok string  `json:"original_token"`
	OrigLvl string  `json:"original_level"`
}

// Capture is the immutable imported bundle of digital edges.
type Capture struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Bus         string            `json:"bus"` // i2c | spi | sync
	Probe       ProbeConfig       `json:"probe"`
	Channels    []Channel         `json:"channels"`
	Edges       map[string][]Edge `json:"edges"`
	Diagnostics []Diagnostic      `json:"diagnostics,omitempty"`
	// SourceDigest is the digest of the canonicalized raw payload.
	SourceDigest string `json:"source_digest"`
	// Origin records how this capture was born: "import" or "source_patch".
	Origin string `json:"origin,omitempty"`
	// PatchedFrom names the parent capture for source-patch descendants.
	PatchedFrom string `json:"patched_from,omitempty"`
	// PatchNote describes the single direct source change.
	PatchNote string `json:"patch_note,omitempty"`
}

// Channel returns the channel by id.
func (c *Capture) Channel(id string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].ID == id {
			return &c.Channels[i]
		}
	}
	return nil
}

// EdgeCount is a cheap total-edge count used by budgets and summaries.
func (c *Capture) EdgeCount() int {
	n := 0
	for _, es := range c.Edges {
		n += len(es)
	}
	return n
}

// TimeWindow returns [min,max] normalized edge times across channels.
func (c *Capture) TimeWindow() (float64, float64) {
	mn, mx := 0.0, 0.0
	first := true
	keys := make([]string, 0, len(c.Edges))
	for k := range c.Edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, e := range c.Edges[k] {
			if first || e.TimeNS < mn {
				mn = e.TimeNS
			}
			if first || e.TimeNS > mx {
				mx = e.TimeNS
			}
			first = false
		}
	}
	return mn, mx
}

// ParamSet is a versioned, immutable bundle of decoder parameters. Clock
// polarity here is the *decoder interpretation* polarity; the raw capture
// clock polarity flag lives forever in the capture's probe/channel data.
type ParamSet struct {
	ID      string `json:"id"`
	Bus     string `json:"bus"`
	Version int    `json:"version"`
	// I2C
	I2CPolarity    string  `json:"i2c_polarity,omitempty"` // scl polarity interpretation: normal|inverted
	SCLChannel     string  `json:"scl_channel,omitempty"`
	SDAChannel     string  `json:"sda_channel,omitempty"`
	GapThresholdNS float64 `json:"gap_threshold_ns,omitempty"`
	// SPI
	SPIChannel  string   `json:"spi_channel,omitempty"` // sclk
	MOSIChannel string   `json:"mosi_channel,omitempty"`
	MISOChannel string   `json:"miso_channel,omitempty"`
	CSChannels  []string `json:"cs_channels,omitempty"`
	// SPIMode "0".."3"; empty => mode unknown, keep all legal candidates.
	SPIMode  string `json:"spi_mode,omitempty"`
	BitOrder string `json:"bit_order,omitempty"` // msb|lsb
	// Sync custom bus
	DataChannel  string `json:"data_channel,omitempty"`
	ValidChannel string `json:"valid_channel,omitempty"`
	WordBits     int    `json:"word_bits,omitempty"`
	SampleEdge   string `json:"sample_edge,omitempty"` // rising|falling
	// BoundarySampling controls whether samples coincident with a data edge
	// take the value before or after the edge. "before"|"after"; empty=before.
	BoundarySampling string `json:"boundary_sampling,omitempty"`
	Note             string `json:"note,omitempty"`
}

// Mode3 is the view of the world at one point on the event timeline.
type Mode3 struct {
	ProjectID    string
	HeadEventSeq int64
	Captures     map[string]*Capture
	Params       map[string]*ParamSet // param set id -> head revision
	ParamHistory []*ParamSet          // every revision in order
	Runs         map[string]*Run
	Candidates   map[string]*Candidate
	Transactions map[string]*Transaction
	Bytes        map[string]*Byte
	Decisions    map[string]*Decision
	Events       []*Event
	// DecisionsByCandidate supports conflict detection.
	DecisionsByCandidate map[string][]*Decision
}

// Run is one decoder execution over one capture with one parameter revision.
type Run struct {
	ID             string       `json:"id"`
	CaptureID      string       `json:"capture_id"`
	ParamID        string       `json:"param_id"`
	ParamVersion   int          `json:"param_version"`
	DecoderVersion string       `json:"decoder_version"`
	Class          ResultClass  `json:"class"`
	InputSummary   InputSummary `json:"input_summary"`
	CandidateIDs   []string     `json:"candidate_ids"`
	DiagnosticIDs  []string     `json:"diagnostic_refs,omitempty"`
	// Diagnostics are stored inline as immutable records of this run.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

// InputSummary pins the exact inputs of a derived result.
type InputSummary struct {
	SourceDigest string  `json:"source_digest"`
	EdgeCount    int     `json:"edge_count"`
	TimeMinNS    float64 `json:"time_min_ns"`
	TimeMaxNS    float64 `json:"time_max_ns"`
	// ParamDigest covers the full parameter set content.
	ParamDigest string `json:"param_digest"`
}

// Candidate is one legal decoding of a capture. Ambiguous decodes (unknown SPI
// mode, unresolved clock polarity) retain *every* legal candidate instead of
// guessing; confidence plus evidence drives reviewer choice.
type Candidate struct {
	ID             string   `json:"id"`
	RunID          string   `json:"run_id"`
	CaptureID      string   `json:"capture_id"`
	Kind           string   `json:"kind"` // e.g. "i2c", "spi-mode0", "sync-rising"
	Label          string   `json:"label"`
	Confidence     float64  `json:"confidence"` // 0..1
	TransactionIDs []string `json:"transaction_ids"`
	// AmbiguityReasons names why this candidate coexists with others.
	AmbiguityReasons []string `json:"ambiguity_reasons,omitempty"`
	Status           string   `json:"status"` // open|accepted|rejected
	Adopted          bool     `json:"adopted"`
}

// Transaction is one reassembled bus transaction. Data gaps never join two
// pieces into one transaction; they terminate the fragment with a diagnostic.
type Transaction struct {
	ID          string  `json:"id"`
	CandidateID string  `json:"candidate_id"`
	CaptureID   string  `json:"capture_id"`
	Index       int     `json:"index"`
	Bus         string  `json:"bus"`
	Kind        string  `json:"kind"` // read|write|custom|fragment
	StartNS     float64 `json:"start_ns"`
	EndNS       float64 `json:"end_ns"`
	// DeviceID is the addressed I2C address or the active CS channel for SPI.
	DeviceID string   `json:"device_id,omitempty"`
	ByteIDs  []string `json:"byte_ids"`
	// DiagnosticCodes are gap/missing-edge/stretch markers attached here.
	DiagnosticCodes []string `json:"diagnostic_codes,omitempty"`
}

// Byte is one decoded byte with an unbroken trail back to the edges that
// produced it (channel -> edge ids + sample window).
type Byte struct {
	ID            string  `json:"id"`
	CandidateID   string  `json:"candidate_id"`
	TransactionID string  `json:"transaction_id"`
	Index         int     `json:"byte_index"`
	Value         int     `json:"value"`         // 0..255
	Role          string  `json:"role"`          // address|data|ack|nack
	Ack           string  `json:"ack,omitempty"` // ACK|NACK
	StartNS       float64 `json:"start_ns"`
	EndNS         float64 `json:"end_ns"`
	// SampleEdges records [dataEdge, clockEdge] edge ids used per bit when
	// known, so any displayed byte returns to raw edges.
	SampleRefs []SampleRef `json:"sample_refs,omitempty"`
}

// SampleRef is one bit's evidence pointer.
type SampleRef struct {
	Bit       int     `json:"bit"`
	ClockEdge string  `json:"clock_edge"`
	DataEdge  string  `json:"data_edge,omitempty"`
	DataLevel int     `json:"data_level"`
	SampleNS  float64 `json:"sample_ns"`
	// Boundary is true when the sample was coincident with a data edge and the
	// boundary-sampling policy decided the level.
	Boundary bool `json:"boundary,omitempty"`
}

// Decision records a reviewer accept/reject/parameter change.
type Decision struct {
	ID          string `json:"id"`
	EventSeq    int64  `json:"event_seq"`
	CandidateID string `json:"candidate_id,omitempty"`
	Action      string `json:"action"` // accept|reject|param_revise|source_patch
	Reason      string `json:"reason"`
	// Conflict is set when the decision contradicts an earlier one on the same
	// candidate; both stay on record.
	Conflict bool `json:"conflict,omitempty"`
}

// Event is the append-only history entry. Every state-changing action appends
// exactly one event. HashPrev/HashSelf make the chain tamper-evident and pin
// parent dependencies for historical replay.
type Event struct {
	Seq       int64          `json:"seq"`
	Type      string         `json:"type"` // project_created|capture_imported|param_revised|run_decoded|decision_recorded|candidate_adopted|source_patched|archive_built
	At        string         `json:"at"`
	Actor     string         `json:"actor"`
	CaptureID string         `json:"capture_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	ParentSeq int64          `json:"parent_seq,omitempty"`
	HashPrev  string         `json:"hash_prev"`
	HashSelf  string         `json:"hash_self"`
}

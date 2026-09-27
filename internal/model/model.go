// Package model defines the domain types for the edge-decoding workbench.
// Layers:
//   - Immutable materials: raw edge streams and probe configs, stored verbatim.
//   - Derived layer: normalized levels, decode candidates, transactions, bytes.
//   - Decision layer: human accept/reject/param-revision events.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ---------- Immutable material: raw edge stream ----------

// Edge is a single digital transition. TimeNs is the raw edge time in
// nanoseconds on the acquisition time base; Level is the raw level (0/1)
// AFTER the transition, exactly as imported (inversion NOT applied).
type Edge struct {
	TimeNs int64 `json:"time_ns"`
	Level  int   `json:"level"`
}

// Channel is one probed digital line. Raw representation is preserved on
// import; normalization (invert, polarity) happens only in the derived layer.
type Channel struct {
	Name         string `json:"name"`          // alias, e.g. "SDA", "SCL", "CS0"
	Role         string `json:"role"`          // sda|scl|sclk|mosi|miso|cs|clk|data
	Invert       bool   `json:"invert"`        // electrical inversion flag from probe config
	InitialLevel int    `json:"initial_level"` // -1 = derive from first edge
	Edges        []Edge `json:"edges"`
}

// EdgeStream is an immutable imported material.
type EdgeStream struct {
	StreamID     string    `json:"stream_id"`
	SampleRateHz float64   `json:"sample_rate_hz"`
	Channels     []Channel `json:"channels"`
}

// ChannelByRole returns the first channel with the given role.
func (s *EdgeStream) ChannelByRole(role string) *Channel {
	for i := range s.Channels {
		if s.Channels[i].Role == role {
			return &s.Channels[i]
		}
	}
	return nil
}

// ChannelsByRole returns all channels whose role has the given prefix
// (so "cs" matches "cs", "cs0", "cs1"...).
func (s *EdgeStream) ChannelsByRole(prefix string) []*Channel {
	var out []*Channel
	for i := range s.Channels {
		r := s.Channels[i].Role
		if r == prefix || (len(r) > len(prefix) && r[:len(prefix)] == prefix && isDigit(r[len(prefix)])) {
			out = append(out, &s.Channels[i])
		}
	}
	return out
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// ---------- Probe configuration (versioned parameters) ----------

type ProbeConfig struct {
	Version            int    `json:"version"`
	ClockPolarity      string `json:"clock_polarity"` // normal|inverted|unknown
	SPIMode            string `json:"spi_mode"`       // "0".."3" or "unknown"
	CSActiveLevel      int    `json:"cs_active_level"`
	SyncWordWidth      int    `json:"sync_word_width"`
	SyncSampleEdge     string `json:"sync_sample_edge"` // rising|falling|unknown
	DecoderVersion     string `json:"decoder_version"`
	BoundaryEpsilonNs  int64  `json:"boundary_epsilon_ns"`
	GapThresholdNs     int64  `json:"gap_threshold_ns"`
	StretchThresholdNs int64  `json:"stretch_threshold_ns"`
	BudgetMaxEdges     int    `json:"budget_max_edges"`
	BudgetMaxCandidates int   `json:"budget_max_candidates"`
}

// Hash returns a stable content hash of the parameters.
func (c ProbeConfig) Hash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// ---------- Derived layer: decode results ----------

// Decode status values. Resource-budget exhaustion, corrupt format and
// semantic no-solution are three distinct outcomes.
const (
	StatusOK              = "ok"
	StatusBudgetExhausted = "budget_exhausted"
	StatusFormatError     = "format_error"
	StatusNoSolution      = "no_solution"
)

// Diagnostic is a positioned problem report. Incomplete input yields
// diagnostics with locations; empty values never masquerade as success.
type Diagnostic struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Channel   string `json:"channel,omitempty"`
	EdgeIndex int    `json:"edge_index,omitempty"`
	TimeNs    int64  `json:"time_ns,omitempty"`
}

// Span locates evidence on a channel.
type Span struct {
	Channel   string `json:"channel"`
	EdgeStart int    `json:"edge_start"`
	EdgeEnd   int    `json:"edge_end"`
	StartNs   int64  `json:"start_ns"`
	EndNs     int64  `json:"end_ns"`
}

// Judgment is a machine-readable record of a decoding judgement, e.g.
// retaining multiple candidates or refusing to stitch across a data gap.
type Judgment struct {
	Code     string   `json:"code"` // CLOCK_MISSING_EDGE | REPEATED_START | CLOCK_STRETCH | SPI_MODE_UNKNOWN | DATA_GAP_NO_STITCH | BOUNDARY_SAMPLE | MULTI_CS | MULTI_CANDIDATE
	Message  string   `json:"message"`
	Span     Span     `json:"span"`
	Affected []string `json:"affected"` // byte/transaction IDs
}

// Judgment codes.
const (
	JClockMissingEdge = "CLOCK_MISSING_EDGE"
	JRepeatedStart    = "REPEATED_START"
	JClockStretch     = "CLOCK_STRETCH"
	JSPIModeUnknown   = "SPI_MODE_UNKNOWN"
	JDataGapNoStitch  = "DATA_GAP_NO_STITCH"
	JBoundarySample   = "BOUNDARY_SAMPLE"
	JMultiCS          = "MULTI_CS"
	JMultiCandidate   = "MULTI_CANDIDATE"
	JClockPolarity    = "CLOCK_POLARITY"
)

// EdgeRef points back from a decoded byte to the raw edges that produced it.
type EdgeRef struct {
	Channel string `json:"channel"`
	Index   int    `json:"index"`
	TimeNs  int64  `json:"time_ns"`
}

// Byte is one decoded byte/word with stable identity and provenance.
type Byte struct {
	ID         string    `json:"id"`
	Value      int       `json:"value"`
	Width      int       `json:"width"`
	Ack        *bool     `json:"ack,omitempty"` // I2C only: true=ACK false=NACK
	Direction  string    `json:"direction"`     // mosi|miso|write|read|data
	EdgeRefs   []EdgeRef `json:"edge_refs"`
	StartNs    int64     `json:"start_ns"`
	EndNs      int64     `json:"end_ns"`
	Confidence float64   `json:"confidence"`
	Flags      []string  `json:"flags,omitempty"`
}

// Transaction is a bus transaction; a data gap never stitches bytes into
// one transaction.
type Transaction struct {
	ID      string   `json:"id"`
	Device  string   `json:"device"` // i2c address like "0x50" or cs name
	StartNs int64    `json:"start_ns"`
	EndNs   int64    `json:"end_ns"`
	Bytes   []Byte   `json:"bytes"`
	Flags   []string `json:"flags,omitempty"`
}

// Candidate is one legal decoding of the material under one parameter
// hypothesis. Ambiguity yields multiple retained candidates.
type Candidate struct {
	ID           string            `json:"id"`
	Protocol     string            `json:"protocol"` // i2c|spi|sync
	Rank         int               `json:"rank"`
	Confidence   float64           `json:"confidence"`
	Params       map[string]string `json:"params"` // hypothesis, e.g. {"spi_mode":"0"}
	Transactions []Transaction     `json:"transactions"`
	Reasons      []string          `json:"reasons"` // judgment codes that spawned this candidate
}

// DecodeResult is the full derived output for one (materials, params) pair.
type DecodeResult struct {
	Status      string       `json:"status"`
	Candidates  []Candidate  `json:"candidates"`
	Judgments   []Judgment   `json:"judgments"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// ---------- Stable identity ----------

// ShortHash hashes parts into a short stable hex id.
func ShortHash(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ByteID derives a stable identity from the material, protocol and the raw
// edges backing the byte. Content-based, so it survives export/import.
func ByteID(materialID, protocol string, refs []EdgeRef, value int) string {
	return "B" + ShortHash(materialID, protocol, refs, value)
}

// TransactionID derives a stable identity from member byte IDs.
func TransactionID(protocol, device string, byteIDs []string, startNs int64) string {
	return "T" + ShortHash(protocol, device, byteIDs, startNs)
}

// CandidateID derives a stable identity from content.
func CandidateID(protocol string, params map[string]string, txnIDs []string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	kv := make([]string, 0, len(keys))
	for _, k := range keys {
		kv = append(kv, k+"="+params[k])
	}
	return "C" + ShortHash(protocol, kv, txnIDs)
}

// MaterialID computes the content hash of a stored material.
func MaterialID(kind string, content json.RawMessage) string {
	return "M" + ShortHash(kind, string(content))
}

// ---------- Events (decision layer) ----------

// Event types.
const (
	EvImportMaterial = "import_material"
	EvParamRevision  = "param_revision"
	EvDecode         = "decode"
	EvDecision       = "decision"
	EvSourceChange   = "source_change"
	EvImportArchive  = "import_archive"
)

// Event is one appended record in the decision log. IDs chain via PrevID
// so history is tamper-evident and replayable.
type Event struct {
	Seq     int64           `json:"seq"`
	ID      string          `json:"id"`
	PrevID  string          `json:"prev_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// DecisionPayload records a human accept/reject of a candidate.
type DecisionPayload struct {
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // accept|reject
	Note        string `json:"note"`
}

// ParamRevisionPayload records a parameter change; old decodes stay
// addressable by their params hash so history is never overwritten.
type ParamRevisionPayload struct {
	Config ProbeConfig `json:"config"`
	Reason string      `json:"reason"`
}

// DecodePayload links a decode run to its inputs.
type DecodePayload struct {
	DecodeID       string   `json:"decode_id"`
	MaterialIDs    []string `json:"material_ids"`
	ParamsHash     string   `json:"params_hash"`
	DecoderVersion string   `json:"decoder_version"`
	Status         string   `json:"status"`
}

// SourceChangePayload records a localised source-data change: a new
// material version that supersedes only part of the input set.
type SourceChangePayload struct {
	NewMaterialID  string `json:"new_material_id"`
	SupersedesID   string `json:"supersedes_id"`
	AffectedScope  string `json:"affected_scope"` // e.g. "channel:CS1"
	Reason         string `json:"reason"`
}

// ImportMaterialPayload ties an import event to its material.
type ImportMaterialPayload struct {
	MaterialID string `json:"material_id"`
	Kind       string `json:"kind"`
}

func (e Event) String() string {
	return fmt.Sprintf("#%d %s %s", e.Seq, e.Type, e.ID[:12])
}

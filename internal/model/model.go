// Package model defines the domain types for the edge-decoding workbench.
//
// Layering rule: sources (captures, probe configs) are immutable evidence.
// Runs, candidates, transactions and bytes are derived: every derived object
// records its input digest, parameter version and parent dependency.
// Human decisions are append-only events; history is never overwritten.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ---------- immutable source layer ----------

// Edge is a single digital transition on one channel.
// T is the original edge time in nanoseconds relative to capture start.
type Edge struct {
	Ch    string `json:"ch"`
	T     int64  `json:"t_ns"`
	Level int    `json:"level"` // 0 or 1, as recorded (pre-inversion)
}

// ChannelDef describes one probe channel. RawAlias is the alias exactly as
// imported; Inverted records hardware-level inversion and is applied only in
// the derived layer.
type ChannelDef struct {
	Name     string `json:"name"`
	RawAlias string `json:"raw_alias"`
	Role     string `json:"role"` // sda|scl|sclk|mosi|miso|cs|dclk|dat|other
	Inverted bool   `json:"inverted"`
}

// Capture is an immutable edge stream plus probe configuration snapshot.
type Capture struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	SampleRateHz float64      `json:"sample_rate_hz"`
	Channels     []ChannelDef `json:"channels"`
	Edges        []Edge       `json:"edges"`
	Note         string       `json:"note,omitempty"`
}

// ProbeConfig is an immutable decoder-parameter snapshot.
type ProbeConfig struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ClockPolarity      string `json:"clock_polarity"` // rising|falling
	DecoderVersion     string `json:"decoder_version"`
	GapThresholdNs     int64  `json:"gap_threshold_ns"`
	StretchThresholdNs int64  `json:"stretch_threshold_ns"`
	SPIMode            string `json:"spi_mode"` // auto|0|1|2|3
	WordSize           int    `json:"word_size"`
	Note               string `json:"note,omitempty"`
}

// ---------- diagnostics ----------

// Distinct failure outcomes. A run ends in exactly one RunStatus; diagnostics
// always carry a position so failures are never disguised as empty success.
const (
	CodeFormatCorrupt   = "format_corrupt"
	CodeBudgetExhausted = "budget_exhausted"
	CodeUnsolvable      = "semantically_unsolvable"
	CodeMissingChannel  = "missing_channel"
	CodeEdgeBounce      = "edge_bounce"
	CodeDataGap         = "data_gap"
	CodeMissingClock    = "missing_clock_edge"
	CodeClockStretch    = "clock_stretch"
	CodeRepeatedStart   = "repeated_start"
	CodeBoundarySample  = "boundary_sample"
	CodeSPIModeUnknown  = "spi_mode_unknown"
	CodeMultiDeviceCS   = "multi_device_cs"
)

type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"` // error|warn|info
	Message   string `json:"message"`
	Channel   string `json:"channel,omitempty"`
	EdgeIndex int    `json:"edge_index,omitempty"`
	TimeNs    int64  `json:"time_ns,omitempty"`
}

// ---------- derived layer ----------

const (
	RunOK              = "ok"
	RunBudgetExhausted = "budget_exhausted"
	RunFormatCorrupt   = "format_corrupt"
	RunUnsolvable      = "semantically_unsolvable"
)

// Run is one decode computation. InputDigest binds capture+config+params;
// ParentRun links revisions so a reviewer can diff dependency versions.
type Run struct {
	ID             string       `json:"id"`
	DecoderVersion string       `json:"decoder_version"`
	ParamVersion   int          `json:"param_version"`
	InputDigest    string       `json:"input_digest"`
	ParentRun      string       `json:"parent_run,omitempty"`
	CaptureID      string       `json:"capture_id"`
	ConfigID       string       `json:"config_id"`
	Status         string       `json:"status"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
}

// Candidate is one legal decoding of the capture. Ambiguous situations
// (missing clock edge, unknown SPI mode, boundary sample) keep several
// candidates instead of forcing one answer.
type Candidate struct {
	ID         string        `json:"id"`
	RunID      string        `json:"run_id"`
	Protocol   string        `json:"protocol"` // i2c|spi|sync
	Rank       int           `json:"rank"`
	Confidence float64       `json:"confidence"`
	Reasons    []string      `json:"reasons"` // machine-readable judgement basis
	Txs        []Transaction `json:"txs"`
}

// Annotation is a machine-readable in-transaction event (stretch, gap...).
type Annotation struct {
	Type    string `json:"type"`
	TimeNs  int64  `json:"time_ns"`
	DurNs   int64  `json:"dur_ns,omitempty"`
	ByteIdx int    `json:"byte_idx,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type Transaction struct {
	ID             string       `json:"id"`
	Device         string       `json:"device"` // i2c addr or cs alias
	StartNs        int64        `json:"start_ns"`
	EndNs          int64        `json:"end_ns"`
	RepeatedStart  bool         `json:"repeated_start,omitempty"`
	TruncatedByGap bool         `json:"truncated_by_gap,omitempty"`
	Bytes          []Byte       `json:"bytes"`
	Annotations    []Annotation `json:"annotations,omitempty"`
}

// Byte carries stable identity: ID is run/candidate/tx/byte-addressable and
// survives export/import round trips. EdgeIdx0..1 point back into the raw
// capture edge list so any byte can be traced to its edges.
type Byte struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Value    int    `json:"value"`
	Masked   bool   `json:"masked,omitempty"` // some bits unknown (missing clock edge)
	Ack      *bool  `json:"ack,omitempty"`    // i2c only: true=ACK false=NACK
	StartNs  int64  `json:"start_ns"`
	EndNs    int64  `json:"end_ns"`
	EdgeIdx0 int    `json:"edge_idx0"`
	EdgeIdx1 int    `json:"edge_idx1"`
}

// ---------- events (decision layer) ----------

const (
	EvSourceImported  = "source_imported"
	EvDecodeRun       = "decode_run"
	EvDecision        = "decision"
	EvParamRevision   = "param_revision"
	EvSourceRevision  = "source_revision"
	EvImportRecovered = "import_recovered"
)

type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt int64           `json:"created_at"`
}

// DecisionPayload is the body of an EvDecision event. Accept and reject of
// the same candidate by different reviewers are both kept and surfaced as a
// conflict; neither overwrites the other.
type DecisionPayload struct {
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // accept|reject
	Reviewer    string `json:"reviewer"`
	Note        string `json:"note,omitempty"`
}

// ---------- digests / ids ----------

func Digest(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func ShortDigest(parts ...any) string { return Digest(parts...)[:12] }

func CaptureID(c *Capture) string    { return "src-cap-" + ShortDigest(c) }
func ConfigID(c *ProbeConfig) string { return "src-cfg-" + ShortDigest(c) }

func RunID(captureID, configID string, paramVersion int, decoderVersion, parent string) string {
	return "run-" + ShortDigest(captureID, configID, paramVersion, decoderVersion, parent)
}

// SortCandidates gives equivalent inputs a stable order: confidence desc,
// then protocol, then first reason, then id. IDs are (re)assigned after sort.
func SortCandidates(cs []*Candidate, runID string) {
	sort.SliceStable(cs, func(i, j int) bool {
		a, b := cs[i], cs[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		ra, rb := "", ""
		if len(a.Reasons) > 0 {
			ra = a.Reasons[0]
		}
		if len(b.Reasons) > 0 {
			rb = b.Reasons[0]
		}
		return ra < rb
	})
	for i, c := range cs {
		c.Rank = i
		c.ID = fmt.Sprintf("%s/c%d", runID, i)
		c.RunID = runID
		for ti := range c.Txs {
			tx := &c.Txs[ti]
			tx.ID = fmt.Sprintf("%s/t%d", c.ID, ti)
			for bi := range tx.Bytes {
				tx.Bytes[bi].ID = fmt.Sprintf("%s/b%d", tx.ID, bi)
				tx.Bytes[bi].Index = bi
			}
		}
	}
}

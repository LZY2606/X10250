package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Quantity is a domain measurement that keeps its original representation
// (string, exactly as imported) together with the normalized SI value used by
// the derived layer. Raw is never dropped: a value that cannot be converted
// stays at its original position with a diagnostic and NormalValid=false.
type Quantity struct {
	Raw         string  `json:"raw"`
	Normal      float64 `json:"normal"`
	Unit        string  `json:"unit"`
	NormalValid bool    `json:"normal_valid"`
}

// ChannelSpec is immutable probe/channel configuration material.
type ChannelSpec struct {
	ID         string   `json:"id"`
	Alias      string   `json:"alias"`
	Probe      string   `json:"probe"`
	Role       string   `json:"role"`
	Inverted   bool     `json:"inverted"`
	InitialRaw *int     `json:"initial_raw,omitempty"`
	Threshold  Quantity `json:"threshold"`
}

// Edge is an immutable captured digital transition. Time keeps the imported
// string and unit; TimeNS is the derived normalized representation.
type Edge struct {
	ID         string   `json:"id"`
	Seq        int      `json:"seq"`
	Channel    string   `json:"channel"`
	Time       Quantity `json:"time"`
	LevelRaw   int      `json:"level_raw"`
	LevelLogic int      `json:"level_logic"`
	Origin     string   `json:"origin"`
}

// Params are the immutable capture parameters / probe configuration.
// ClockPolarity belongs to the source layer (it is retained, not invented).
type Params struct {
	SampleRate     Quantity `json:"sample_rate"`
	TimeBase       string   `json:"time_base"`
	CoordinateBase string   `json:"coordinate_base"`
	ClockPolarity  string   `json:"clock_polarity"` // rising|falling|unknown
	ClockPhase     string   `json:"clock_phase"`    // sample|shift|unknown (SPI/自定义)
	SpiMode        string   `json:"spi_mode"`       // 0|1|2|3|unknown
	BitOrder       string   `json:"bit_order"`      // msb|lsb
	StretchNS      int64    `json:"stretch_ns"`
	GapNS          int64    `json:"gap_ns"`
	ActiveLevel    string   `json:"active_level"`
	SyncWord       int      `json:"sync_word"`
	SyncBits       int      `json:"sync_bits"`
}

// Capture is one immutable revision of imported digital material.
type Capture struct {
	ID          string        `json:"id"`
	Revision    int           `json:"revision"`
	Protocol    string        `json:"protocol"` // i2c|spi|sync
	DecoderVer  string        `json:"decoder_version"`
	Params      Params        `json:"params"`
	Channels    []ChannelSpec `json:"channels"`
	Edges       []Edge        `json:"edges"`
	Origin      string        `json:"origin"`
	Diagnostics []Diagnostic  `json:"diagnostics"`
}

// Diagnostic points at a concrete position (JSON pointer or source locator).
type Diagnostic struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Pos      int    `json:"pos"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error|warning
}

// Byte is a derived decoded byte. EdgeIDs lets any displayed byte walk back to
// the exact source edges that produced it.
type Byte struct {
	ID          string   `json:"id"`
	Index       int      `json:"index"`
	Value       int      `json:"value"`
	Hex         string   `json:"hex"`
	Kind        string   `json:"kind"` // address|data
	StartNS     int64    `json:"start_ns"`
	EndNS       int64    `json:"end_ns"`
	ClockEdges  []string `json:"clock_edges"`
	FrameEdges  []string `json:"frame_edges"`
	Ack         string   `json:"ack"` // ACK|NACK|""
	AckEdges    []string `json:"ack_edges"`
	Valid       bool     `json:"valid"`
	InvalidCode string   `json:"invalid_code"`
}

// Candidate is one legal decoding of one transaction. Ambiguous cases keep
// every legal candidate; confidence is part of derived evidence.
type Candidate struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	DecoderVer string   `json:"decoder_version"`
	Mode       string   `json:"mode"`
	Confidence float64  `json:"confidence"`
	Bytes      []Byte   `json:"bytes"`
	Accepted   bool     `json:"accepted"`
	Rejected   bool     `json:"rejected"`
	Note       string   `json:"note"`
	EdgeIDs    []string `json:"edge_ids"`
}

// Transaction is a derived bus transaction. Gaps never stitch transactions.
type Transaction struct {
	ID         string      `json:"id"`
	Index      int         `json:"index"`
	Protocol   string      `json:"protocol"`
	Device     string      `json:"device"`
	StartNS    int64       `json:"start_ns"`
	EndNS      int64       `json:"end_ns"`
	Status     string      `json:"status"` // complete|incomplete|infeasible
	Segments   int         `json:"segments"`
	Candidate  []Candidate `json:"candidates"`
	FindingIDs []string    `json:"finding_ids"`
	EdgeIDs    []string    `json:"edge_ids"`
}

// Finding is the machine-readable judgement/evidence record.
type Finding struct {
	ID       string `json:"id"`
	Code     string `json:"code"`
	Kind     string `json:"kind"`     // state|diagnostic|decision
	Severity string `json:"severity"` // info|warning|error
	StartNS  int64  `json:"start_ns"`
	EndNS    int64  `json:"end_ns"`
	Channel  string `json:"channel"`
	Detail   string `json:"detail"`
	RefEdge  string `json:"ref_edge"`
	RefTxn   string `json:"ref_txn"`
	RefCand  string `json:"ref_candidate"`
}

// DecodeResult is the full derived layer for one capture revision.
type DecodeResult struct {
	CaptureID    string        `json:"capture_id"`
	Revision     int           `json:"revision"`
	DecoderVer   string        `json:"decoder_version"`
	InputSummary InputSummary  `json:"input_summary"`
	Transactions []Transaction `json:"transactions"`
	Findings     []Finding     `json:"findings"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
}

// InputSummary is the deterministic fingerprint of what was decoded.
type InputSummary struct {
	CaptureHash string `json:"capture_hash"`
	ParamsHash  string `json:"params_hash"`
	EdgeCount   int    `json:"edge_count"`
	RangeStart  int64  `json:"range_start_ns"`
	RangeEnd    int64  `json:"range_end_ns"`
	ValidEdges  int    `json:"valid_edges"`
}

// DecisionOp is a reviewer-modifiable decision layered over immutable
// material/derived results.
type DecisionOp struct {
	Action string `json:"action"` // accept|reject|adjust
	CandID string `json:"candidate_id"`
	TxnID  string `json:"txn_id"`
	Mode   string `json:"mode"`
	Reason string `json:"reason"`
}

// CanonicalHash returns a stable hash of the JSON encoding of v.
func CanonicalHash(prefix string, v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return prefix + hex.EncodeToString(sum[:])[:16]
}

func hashNS(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return prefix + hex.EncodeToString(h.Sum(nil))[:16]
}

// ---- typed errors: budget exhaustion / corrupt format / semantic no-solution ----

// BudgetError means a declared resource budget was exhausted.
type BudgetError struct {
	What  string
	Limit int
}

func (e *BudgetError) Error() string {
	return fmt.Sprintf("resource budget exhausted: %s (limit %d)", e.What, e.Limit)
}

// CorruptError means the container/format itself is damaged, with position.
type CorruptError struct {
	Path    string
	Pos     int
	Line    int
	Column  int
	Message string
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("corrupt format at %s pos %d (line %d col %d): %s", e.Path, e.Pos, e.Line, e.Column, e.Message)
}

// SemanticError means the input parsed but has no feasible decoding solution.
type SemanticError struct {
	Diagnostics []Diagnostic
}

func (e *SemanticError) Error() string {
	var b strings.Builder
	b.WriteString("semantically infeasible input:")
	for _, d := range e.Diagnostics {
		fmt.Fprintf(&b, " %s@%s:%s", d.Code, d.Path, d.Message)
	}
	return b.String()
}

var (
	timeNumUnit = regexp.MustCompile(`^\s*([+-]?(?:\d+(?:\.\d*)?|\.\d+))\s*([pnumµμ]?s|ns|us|ms|s|sample|samples)?\s*$`)
)

// unitScaleNS maps supported time units to nanoseconds.
var unitScaleNS = map[string]float64{
	"ps":      1e-3,
	"ns":      1,
	"us":      1e3,
	"µs":      1e3,
	"μs":      1e3,
	"ms":      1e6,
	"s":       1e9,
	"sample":  -1, // resolved against sample rate
	"samples": -1,
	"":        1, // bare numbers in an NS time base
}

// ParseTimeQuantity preserves the raw representation and normalizes to ns.
// An unconvertible value returns NormalValid=false and no error: it stays in
// place and the caller attaches a diagnostic instead of dropping it.
func ParseTimeQuantity(raw, unit, timeBase string, sampleHz float64) Quantity {
	q := Quantity{Raw: raw, Unit: unit}
	m := timeNumUnit.FindStringSubmatch(raw)
	if m == nil {
		return q
	}
	num, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return q
	}
	u := m[2]
	if u == "" {
		u = unit
	}
	if u == "" {
		u = timeBaseUnit(timeBase)
	}
	scale, ok := unitScaleNS[u]
	if !ok {
		return q
	}
	if scale < 0 { // sample index -> seconds via rate
		if sampleHz <= 0 {
			return q
		}
		scale = 1e9 / sampleHz
	}
	q.Normal = num * scale
	q.Unit = u
	q.NormalValid = true
	return q
}

func timeBaseUnit(tb string) string {
	switch strings.ToLower(strings.TrimSpace(tb)) {
	case "ns", "nanoseconds":
		return "ns"
	case "us", "µs", "microseconds":
		return "us"
	case "ms", "milliseconds":
		return "ms"
	case "s", "seconds":
		return "s"
	case "sample", "samples":
		return "sample"
	}
	return "ns"
}

// ParseRate parses a sample rate such as "10 MHz" and returns Hz.
func ParseRate(raw string) (float64, string, bool) {
	m := regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([kKmMgG]?)(?:Hz|hz)?\s*$`).FindStringSubmatch(raw)
	if m == nil {
		return 0, "", false
	}
	num, _ := strconv.ParseFloat(m[1], 64)
	switch strings.ToLower(m[2]) {
	case "k":
		num *= 1e3
	case "m":
		num *= 1e6
	case "g":
		num *= 1e9
	}
	return num, strings.TrimSpace(raw), true
}

// ChannelByAlias resolves a channel id-or-alias.
func (c *Capture) ChannelByAlias(name string) *ChannelSpec {
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.ID == name || ch.Alias == name {
			return ch
		}
	}
	return nil
}

// Package domain holds the immutable material layer (captured edges and probe
// configuration) together with the derived review layer (transactions, bytes,
// candidates and diagnostics).
//
// Every physical quantity keeps its original textual representation. The
// normalized value lives on a separate field and is produced only by the
// derivation layer; values that cannot be converted stay in place with a
// diagnostic instead of being silently replaced.
package domain

import "fmt"

// Base dimensions used by the decoder.
const (
	BaseTime = "time"
)

// KnownResult is the tri-state (plus OK) machine readable outcome required for
// ambiguous / broken input. The four values are intentionally distinct so
// callers can tell resource exhaustion from corruption from genuine semantic
// ambiguity.
type KnownResult string

const (
	ResultOK                 KnownResult = "ok"
	ResultFormatCorrupt      KnownResult = "format_corrupt"
	ResultResourceBudget     KnownResult = "resource_budget"
	ResultSemanticUndecidable KnownResult = "semantic_undecidable"
)

// Measure is a physical value with its original spelling preserved.
type Measure struct {
	Raw    string  `json:"raw"`              // original representation, e.g. "20ns"
	Base   string  `json:"base"`             // dimension, e.g. "time"
	Unit   string  `json:"unit"`             // normalized unit token, e.g. "ns"
	Value  float64 `json:"value,omitempty"`  // normalized value in Unit
	Valid  bool    `json:"valid"`            // conversion succeeded
	Reason string  `json:"reason,omitempty"` // why conversion failed
}

// SampleRate preserves the source declaration (e.g. "100MHz") and the
// normalized hertz value used for coordinate reasoning.
type SampleRate struct {
	Raw    string  `json:"raw"`
	Hertz  float64 `json:"hz,omitempty"`
	Valid  bool    `json:"valid"`
	Reason string  `json:"reason,omitempty"`
}

// ChannelConfig is probe configuration as declared by the capture.
type ChannelConfig struct {
	Key             string `json:"key"`              // ch0 ... source stable key
	Alias           string `json:"alias"`            // human probe label
	Role            string `json:"role"`             // SCL,SDA,SCK,MOSI,MISO,CS,SYNC,CLK,DATA
	Bus             string `json:"bus"`
	Device          string `json:"device,omitempty"` // CS target device
	Inverted        bool   `json:"inverted"`         // level inversion declared by probe
	ClockPolarity   string `json:"clock_polarity,omitempty"` // rising|falling as declared
	CSActive        string `json:"cs_active,omitempty"`      // low|high
	RawLine         string `json:"raw_line"`
	LineNo          int    `json:"line_no"`
}

// Edge is one captured digital transition. Identity is (Rev, Seq), never a
// database row id, so it survives export/import.
type Edge struct {
	Rev      int     `json:"rev"`
	Seq      int     `json:"seq"`
	ChanKey  string  `json:"chan"`
	Time     Measure `json:"time"`
	RawLevel string  `json:"raw_level"` // original token: 0/1/H/L/...
	Level    int     `json:"level"`     // normalized 0/1 after inversion; -1 invalid
	LineNo   int     `json:"line_no"`
	Valid    bool    `json:"valid"`
	Reason   string  `json:"reason,omitempty"`
}

// BusDecl describes a declared bus.
type BusDecl struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // i2c|spi|custom
	Bits int    `json:"bits,omitempty"`
	Order string `json:"order,omitempty"` // msb|lsb
}

// Capture is the full immutable import material.
type Capture struct {
	Rev             int               `json:"rev"`
	Format          string            `json:"format"`
	FormatVersion   string            `json:"format_version"`
	RawText         string            `json:"raw_text"`
	SampleRate      SampleRate        `json:"sample_rate"`
	TimeUnit        string            `json:"time_unit_raw"`
	TimeOrigin      Measure           `json:"time_origin"`
	SourceVersion   string            `json:"source_version,omitempty"` // decoder version declared by source
	RulesVersion    string            `json:"rules_version"`            // rules that decoded it
	Buses           []BusDecl         `json:"buses"`
	Channels        []ChannelConfig   `json:"channels"`
	Edges           []Edge            `json:"edges"`
}

// Diagnostic carries a machine readable code and a position. Positions are
// line based (capture coordinates) or time based (waveform coordinates).
type Diagnostic struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"` // error|warning|info
	Message  string  `json:"message"`
	Line     int     `json:"line,omitempty"`
	Column   int     `json:"column,omitempty"`
	StartNS  float64 `json:"start_ns,omitempty"`
	EndNS    float64 `json:"end_ns,omitempty"`
	ObjectID string  `json:"object_id,omitempty"`
}

// EvidenceRef points at the exact edges a displayed value was derived from.
type EvidenceRef struct {
	EdgeRev int `json:"rev"`
	EdgeSeq int `json:"seq"`
}

// Candidate is one legal decoding of an ambiguous object.
type Candidate struct {
	ID         string   `json:"id"`
	Hypothesis string   `json:"hypothesis"`
	ValueHex   string   `json:"value_hex"`
	Confidence float64  `json:"confidence"`
	Reasons    []string `json:"reasons"`
	Chosen     bool     `json:"chosen"`
}

const (
	AckACK     = "ACK"
	AckNACK    = "NACK"
	AckMissing = "MISSING"
	AckNA      = "NA"
)

// Byte is one decoded byte with stable identity and edge provenance.
type Byte struct {
	ID         string        `json:"id"`
	Ord        int           `json:"ord"`
	ValueHex   string        `json:"value_hex"`
	ACK        string        `json:"ack"`
	StartNS    float64       `json:"start_ns"`
	EndNS      float64       `json:"end_ns"`
	Confidence float64       `json:"confidence"`
	Ambiguous  bool          `json:"ambiguous"`
	Flags      []string      `json:"flags"`
	Evidence   []EvidenceRef `json:"evidence"`
	Candidates []Candidate   `json:"candidates"`
}

// Transaction groups bytes belonging to one unbroken bus activity region.
type Transaction struct {
	ID          string        `json:"id"`
	Bus         string        `json:"bus"`
	Kind        string        `json:"kind"`
	Device      string        `json:"device,omitempty"`
	StartNS     float64       `json:"start_ns"`
	EndNS       float64       `json:"end_ns"`
	Status      string        `json:"status"` // complete|incomplete_clock|gap_split|no_start|semantic_undecidable|...
	Bytes       []*Byte       `json:"bytes"`
	Candidates  []Candidate   `json:"candidates"`
	Flags       []string      `json:"flags"`
	RepeatedStarts []float64  `json:"repeated_starts_ns,omitempty"`
	Stretches   [][2]float64  `json:"stretches_ns,omitempty"`
	Gaps        [][2]float64  `json:"gaps_ns,omitempty"`
}

// RunView is one immutable decode result produced by a decode event.
type RunView struct {
	EventSeq     int64         `json:"event_seq"`
	RulesVersion string        `json:"rules_version"`
	ParamsVersion string       `json:"params_version"`
	InputDigest  string        `json:"input_digest"`
	Outcome      KnownResult   `json:"outcome"`
	Transactions []*Transaction `json:"transactions"`
	Diagnostics  []Diagnostic  `json:"diagnostics"`
}

// Decision records an accepted / rejected / adjusted review outcome.
type Decision struct {
	EventSeq int64  `json:"event_seq"`
	TargetID string `json:"target_id"`
	Action   string `json:"action"` // accept|reject|choose|param
	Candidate string `json:"candidate_id,omitempty"`
	Note     string `json:"note,omitempty"`
	Branch   string `json:"branch"`
	ParentSeq int64 `json:"parent_seq"`
}

// ProjectState is the reviewable view at one point in history.
type ProjectState struct {
	ProjectID   string                 `json:"project_id"`
	Name        string                 `json:"name"`
	AtSeq       int64                  `json:"at_seq"`
	Branch      string                 `json:"branch"`
	Capture     *Capture               `json:"capture"`
	Run         *RunView               `json:"run"`
	Decisions   []Decision             `json:"decisions"`
	Branches    []string               `json:"branches"`
	HeadSeq     int64                  `json:"head_seq"`
	Params      map[string]string      `json:"params"`
	ValidFromNS float64                `json:"valid_from_ns"`
	ValidToNS   float64                `json:"valid_to_ns"`
}

// StableID helpers keep every displayed value addressable across re-imports.
func TxnID(kind, device string, startSeq int) string {
	if device != "" {
		return fmt.Sprintf("TX:%s:%s:e%d", kind, device, startSeq)
	}
	return fmt.Sprintf("TX:%s:e%d", kind, startSeq)
}

func ByteID(kind, device string, startSeq, ord int) string {
	if device != "" {
		return fmt.Sprintf("B:%s:%s:e%d:%d", kind, device, startSeq, ord)
	}
	return fmt.Sprintf("B:%s:e%d:%d", kind, startSeq, ord)
}

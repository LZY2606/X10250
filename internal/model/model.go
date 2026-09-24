// Package model defines the layered domain objects of the edge decoding
// bench: immutable captured material, versioned decode parameters, derived
// decode results (bytes/transactions/candidates), machine readable evidence
// events and review decisions.
package model

import (
	"fmt"

	"edgedecode/internal/units"
)

// Pos is a position inside an imported source document. Diagnostics are
// always positioned so an unconvertible value can stay in its place.
type Pos struct {
	Line int `json:"line"`
	Col  int `json:"col,omitempty"`
}

func (p Pos) String() string {
	if p.Col > 0 {
		return lineCol(p.Line, p.Col)
	}
	return lineOnly(p.Line)
}

func lineCol(l, c int) string { return formatPos(l, c) }
func lineOnly(l int) string   { return formatPos(l, 0) }

// Diag is a positioned diagnostic. Severity is "error" or "warning".
type Diag struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	Pos      Pos    `json:"pos"`
	Raw      string `json:"raw,omitempty"`
	Hint     string `json:"hint,omitempty"`
}

type Level bool

const (
	Low  Level = false
	High Level = true
)

func (l Level) String() string {
	if l {
		return "1"
	}
	return "0"
}

// Edge is an immutable digital-channel transition. TSec is normalized time in
// seconds (derived); RawTime keeps the source token exactly as imported.
type Edge struct {
	ID        string        `json:"id"`
	Channel   string        `json:"channel"`
	Level     Level         `json:"level"`
	TSec      float64       `json:"tSec"`
	RawTime   string        `json:"rawTime"`
	TimeM     units.Measure `json:"timeMeasure"`
	SampleIdx int64         `json:"sampleIdx,omitempty"`
	Occ       int           `json:"occ"`
	Pos       Pos           `json:"pos"`
	Inverted  bool          `json:"inverted"`
}

// Probe describes how a physical channel was attached. The raw level token
// and inversion flag are kept; normalization to logical level is derived.
type Probe struct {
	Channel    string        `json:"channel"`
	Alias      string        `json:"alias"`
	Inverted   bool          `json:"inverted"`
	ThresholdM units.Measure `json:"thresholdMeasure"`
	Threshold  string        `json:"rawThreshold,omitempty"`
	Pos        Pos           `json:"pos"`
}

// Capture is the immutable material layer of one imported document.
type Capture struct {
	ID          string        `json:"id"`
	DocumentID  string        `json:"documentId"`
	Name        string        `json:"name"`
	SourceText  string        `json:"sourceText"`
	Decoder     string        `json:"decoder"`
	DecoderVer  string        `json:"decoderVersion"`
	ClockPolRaw string        `json:"rawClockPolarity,omitempty"`
	ClockPolPos Pos           `json:"clockPolarityPos,omitempty"`
	SampleRateM units.Measure `json:"sampleRateMeasure"`
	SampleRate  string        `json:"rawSampleRate"`
	TimeBaseM   units.Measure `json:"timeBaseMeasure"`
	TimeBaseRaw string        `json:"rawTimeBase,omitempty"`
	Probes      []Probe       `json:"probes"`
	Edges       []Edge        `json:"edges"`
	Diags       []Diag        `json:"diags"`
	HasErrors   bool          `json:"hasErrors"`
}

// Protocol names.
const (
	ProtoI2C    = "i2c"
	ProtoSPI    = "spi"
	ProtoCustom = "custom-sync"
)

// Params is a versioned, reviewer-modifiable decode configuration. Polarity
// here overrides the (immutable) material layer clock polarity hint.
type Params struct {
	Version        int     `json:"version"`
	Protocol       string  `json:"protocol"`
	ClockPolarity  string  `json:"clockPolarity"`  // i2c: rising|falling sample edge; spi: idle-low|idle-high (CPOL)
	ClockPhase     string  `json:"clockPhase"`     // spi: first|second (CPHA)
	BitOrder       string  `json:"bitOrder"`       // msb|lsb
	ActiveCS       string  `json:"activeCs"`       // low|high
	ActiveEdgeCust string  `json:"activeEdgeCust"` // custom: rising|falling
	FrameBits      int     `json:"frameBits"`      // custom frame width
	GapMaxBitTimes float64 `json:"gapMaxBitTimes"`
	StretchMinUs   float64 `json:"stretchMinUs"`
	StretchTolNs   float64 `json:"stretchTolNs"`
}

func DefaultParams(protocol string) Params {
	return Params{
		Version:        1,
		Protocol:       protocol,
		ClockPolarity:  "rising",
		ClockPhase:     "second",
		BitOrder:       "msb",
		ActiveCS:       "low",
		ActiveEdgeCust: "rising",
		FrameBits:      8,
		GapMaxBitTimes: 14,
		StretchMinUs:   2,
		StretchTolNs:   50,
	}
}

// ParamRevision is an immutable revision of the parameter layer.
type ParamRevision struct {
	ID        string `json:"id"`
	Version   int    `json:"version"`
	Reason    string `json:"reason"`
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt"`
	Params    Params `json:"params"`
	ParentID  string `json:"parentId,omitempty"`
}

// Byte is one decoded byte. EdgeIDs link it back to the exact sampled
// transitions; display values carry stable identities.
type Byte struct {
	ID          string   `json:"id"`
	Index       int      `json:"index"`
	Value       int      `json:"value"`
	Hex         string   `json:"hex"`
	Role        string   `json:"role"` // addr|data|ack|candidate|other
	Ack         string   `json:"ack"`  // ack|nack|unknown|""
	StartSec    float64  `json:"startSec"`
	EndSec      float64  `json:"endSec"`
	RawStart    string   `json:"rawStart"`
	RawEnd      string   `json:"rawEnd"`
	EdgeIDs     []string `json:"edgeIds"`
	CandidateOf string   `json:"candidateOf,omitempty"`
	Confidence  float64  `json:"confidence"`
	Flags       []string `json:"flags"`
}

// Candidate is one legal, non-dominated interpretation kept because the
// evidence does not uniquely decide.
type Candidate struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // spi-mode|boundary|missing-edge
	Label      string   `json:"label"`
	ByteValues []int    `json:"byteValues"`
	ByteIDs    []string `json:"byteIds"`
	Weight     float64  `json:"weight"`
	Confidence float64  `json:"confidence"`
	Chosen     bool     `json:"chosen"`
	Reason     string   `json:"reason"`
	EventIDs   []string `json:"eventIds"`
}

// Transaction groups bytes. ValidStart/EndSec are the evidence-valid interval.
type Transaction struct {
	ID            string   `json:"id"`
	Proto         string   `json:"proto"`
	Index         int      `json:"index"`
	CS            string   `json:"cs,omitempty"`
	Device        string   `json:"device,omitempty"`
	ByteIDs       []string `json:"byteIds"`
	StartSec      float64  `json:"startSec"`
	EndSec        float64  `json:"endSec"`
	RawStart      string   `json:"rawStart"`
	RawEnd        string   `json:"rawEnd"`
	Status        string   `json:"status"` // ok|truncated|unframed
	InvalidReason string   `json:"invalidReason,omitempty"`
	CandidateIDs  []string `json:"candidateIds,omitempty"`
	GroupID       string   `json:"groupId,omitempty"`
	Flags         []string `json:"flags,omitempty"`
}

// Evidence kinds. Structured states, not prose logs.
const (
	EvClockPolarity  = "clock-polarity-applied"
	EvBoundarySample = "boundary-sample"
	EvRepeatedStart  = "repeated-start"
	EvClockStretch   = "clock-stretching"
	EvDataGap        = "data-gap"
	EvMultiCS        = "multi-device-cs"
	EvCandidates     = "parallel-candidates"
	EvMissingClock   = "missing-clock-edge"
)

type Evidence struct {
	ID       string                 `json:"id"`
	Kind     string                 `json:"kind"`
	Proto    string                 `json:"proto"`
	StartSec float64                `json:"startSec"`
	EndSec   float64                `json:"endSec"`
	RawStart string                 `json:"rawStart"`
	RawEnd   string                 `json:"rawEnd"`
	Detail   map[string]interface{} `json:"detail"`
	Affected []string               `json:"affected"`
	ByteIDs  []string               `json:"byteIds,omitempty"`
	EdgeIDs  []string               `json:"edgeIds,omitempty"`
}

// Run is one derivation: input summary + parameter version + parents.
type Run struct {
	ID           string                 `json:"id"`
	Seq          int64                  `json:"seq"`
	CaptureID    string                 `json:"captureId"`
	DocumentID   string                 `json:"documentId"`
	ParamID      string                 `json:"paramId"`
	ParamVersion int                    `json:"paramVersion"`
	InputDigest  string                 `json:"inputDigest"`
	InputSummary map[string]interface{} `json:"inputSummary"`
	ParentRunIDs []string               `json:"parentRunIds"`
	DecoderVer   string                 `json:"decoderVersion"`
	CreatedAt    string                 `json:"createdAt"`
	Outcome      string                 `json:"outcome"` // ok|denied-budget|format-corrupt|semantic-impossible
	Diags        []Diag                 `json:"diags,omitempty"`
	Bytes        []Byte                 `json:"bytes,omitempty"`
	Transactions []Transaction          `json:"transactions,omitempty"`
	Candidates   []Candidate            `json:"candidates,omitempty"`
	Evidence     []Evidence             `json:"evidence,omitempty"`
}

// Decision kinds emitted by the review UI.
const (
	DecAccept      = "accept"
	DecReject      = "reject"
	DecParamChange = "param-change"
	DecConflict    = "conflict"
)

type Decision struct {
	ID            string `json:"id"`
	Seq           int64  `json:"seq"`
	Kind          string `json:"kind"`
	TargetID      string `json:"targetId"` // transaction|byte|candidate|run id
	TargetKind    string `json:"targetKind"`
	Note          string `json:"note"`
	Author        string `json:"author"`
	CreatedAt     string `json:"createdAt"`
	ParamID       string `json:"paramId,omitempty"` // for param-change
	RunID         string `json:"runId,omitempty"`   // resulting run
	ConflictsWith string `json:"conflictsWith,omitempty"`
}

// TimelineItem is the unified, ordered history view.
type TimelineItem struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"` // import|revision|run|decision
	ID        string `json:"id"`
	Label     string `json:"label"`
	CreatedAt string `json:"createdAt"`
}

// Project bundles the full reviewable state.
type Project struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	CreatedAt   string         `json:"createdAt"`
	Documents   []DocumentMeta `json:"documents"`
}

type DocumentMeta struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	FileName   string   `json:"fileName"`
	Digest     string   `json:"digest"`
	ImportedAt string   `json:"importedAt"`
	CaptureIDs []string `json:"captureIds"`
}

func formatPos(line, col int) string {
	if col > 0 {
		return fmt.Sprintf("line %d col %d", line, col)
	}
	return fmt.Sprintf("line %d", line)
}

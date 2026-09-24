// Package domain holds the immutable evidence model and the derived review model
// for the edge decoding bench. Nothing in this package performs I/O.
package domain

import "encoding/json"

// Status codes. The three failure families are deliberately distinct:
// FORMAT_CORRUPT (bytes/structure unusable), SEMANTIC_UNSOLVABLE (well formed
// but no legal decode exists) and RESOURCE_EXHAUSTED (legal interpretations
// exist but the candidate budget was spent before enumeration finished).
const (
	StatusOK                = "OK"
	StatusWarning           = "WARNING"
	StatusFormatCorrupt     = "FORMAT_CORRUPT"
	StatusSemanticUnsolvable = "SEMANTIC_UNSOLVABLE"
	StatusResourceExhausted = "RESOURCE_EXHAUSTED"

	BusI2C  = "I2C"
	BusSPI  = "SPI"
	BusSync = "SYNC"
)

// Diagnostic is a located problem. Position is a human/checkable coordinate
// such as "edge[17].at" or "channel SDA @ sample 412".
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // error | warning
	Position string `json:"position"`
	Message  string `json:"message"`
}

// Quantity preserves the original representation from the probe file. NormPs /
// NormMilliHz are populated only when conversion succeeds; an unconvertible
// value stays in place with Valid=false and a diagnostic instead of being
// dropped or replaced by a default.
type Quantity struct {
	Raw        string      `json:"raw"`
	Unit       string      `json:"unit"`
	Valid      bool        `json:"valid"`
	NormPs     int64       `json:"normPs,omitempty"`
	NormMilliHz int64      `json:"normMilliHz,omitempty"`
	NormMicroV int64       `json:"normMicroV,omitempty"`
	Diagnostic *Diagnostic `json:"diagnostic,omitempty"`
}

// Channel is probe configuration captured verbatim.
type Channel struct {
	ID        string `json:"id"`
	Alias     string `json:"alias"`
	Role      string `json:"role"` // SCL,SDA,SCLK,MOSI,MISO,CS0..,CSYNC,CDATA,CCLK
	Inverted  bool   `json:"inverted"`
	Threshold Quantity `json:"threshold"`
}

// Edge is a single digital transition. Level is the raw probe level AFTER the
// edge; inversion is applied only in the derived physical timeline.
type Edge struct {
	ID        string   `json:"id"`
	Channel   string   `json:"channel"`
	At        Quantity `json:"at"`
	SampleIdx int64    `json:"sampleIdx"`
	Level     int      `json:"level"`
}

// Capture is the immutable material layer.
type Capture struct {
	ID                string             `json:"id"`
	FormatVersion     string             `json:"formatVersion"`
	Version           int                `json:"version"`
	ParentID          string             `json:"parentId,omitempty"`
	SampleRate        Quantity           `json:"sampleRate"`
	SampleIntervalPs  int64              `json:"sampleIntervalPs"`
	ClockPolarityHint map[string]string  `json:"clockPolarityHint"`
	Channels          []Channel          `json:"channels"`
	Edges             []*Edge            `json:"edges"`
	Diagnostics       []*Diagnostic      `json:"diagnostics,omitempty"`
	CreatedBy         string             `json:"createdBy,omitempty"`
	CreatedAt         string             `json:"createdAt"`
}

// Params is a versioned, named decoder rule set. Revisions never overwrite.
type Params struct {
	ID                  string `json:"id"`
	Bus                 string `json:"bus"`
	Version             int    `json:"version"`
	ParentID            string `json:"parentId,omitempty"`
	SampleEdge          string `json:"sampleEdge"` // RISING | FALLING (I2C/SYNC)
	CPOL                string `json:"cpol"`       // low | high | unknown (SPI)
	SPIMode             string `json:"spiMode"`    // MODE0..MODE3 | UNKNOWN
	CSActive            string `json:"csActive"`   // low | high
	BoundaryTolerancePs int64  `json:"boundaryTolerancePs"`
	StretchThresholdPs  int64  `json:"stretchThresholdPs"`
	DataGapThresholdPs  int64  `json:"dataGapThresholdPs"`
	Note                string `json:"note"`
	CreatedBy           string `json:"createdBy,omitempty"`
	CreatedAt           string `json:"createdAt"`
}

type BitRef struct {
	Bit          int   `json:"bit"`
	Value        int   `json:"value"`
	SampleAtPs   int64 `json:"sampleAtPs"`
	ClockEdgeID  string `json:"clockEdgeId"`
	DataEdgeIDs  []string `json:"dataEdgeIds"`
	Boundary     bool  `json:"boundary"`
}

type ByteCandidate struct {
	Key        string  `json:"key"`
	Value      int     `json:"value"`
	UncertainMask int   `json:"uncertainMask"`
	Confidence float64 `json:"confidence"`
	SampleMode string  `json:"sampleMode"`
	Reason     string  `json:"reason"`
}

type ACKResult struct {
	Value       string   `json:"value"` // ACK | NACK | UNKNOWN
	Candidates  []string `json:"candidates,omitempty"`
	ClockEdgeID string   `json:"clockEdgeId,omitempty"`
	SampleAtPs  int64    `json:"sampleAtPs,omitempty"`
}

type Byte struct {
	ID         string          `json:"id"`
	Index      int             `json:"index"`
	Value      int             `json:"value"`
	Candidates []ByteCandidate `json:"candidates"`
	ACK        *ACKResult      `json:"ack,omitempty"`
	BitRefs    []BitRef        `json:"bitRefs"`
	StartPs    int64           `json:"startPs"`
	EndPs      int64           `json:"endPs"`
	InvalidReason string       `json:"invalidReason,omitempty"`
}

type FrameCandidate struct {
	Key        string  `json:"key"`
	Label      string  `json:"label"`
	ByteValues []int   `json:"byteValues"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type Transaction struct {
	ID              string           `json:"id"`
	Bus             string           `json:"bus"`
	Kind            string           `json:"kind"`
	Device          string           `json:"device,omitempty"`
	StartEdgeID     string           `json:"startEdgeId"`
	EndEdgeID       string           `json:"endEdgeId"`
	StartPs         int64            `json:"startPs"`
	EndPs           int64            `json:"endPs"`
	Bytes           []*Byte          `json:"bytes"`
	State           string           `json:"state"` // complete | broken | partial | gap_split | resource_exhausted | unsolvable
	Flags           []string         `json:"flags,omitempty"`
	FrameCandidates []FrameCandidate `json:"frameCandidates,omitempty"`
	LinkedFromTxn   string           `json:"linkedFromTxn,omitempty"`
	EventIDs        []string         `json:"eventIds,omitempty"`
	InvalidReason   string           `json:"invalidReason,omitempty"`
}

type InputSummary struct {
	EdgeCount        int      `json:"edgeCount"`
	ChannelIDs       []string `json:"channelIds"`
	SampleIntervalPs int64    `json:"sampleIntervalPs"`
	TimeMinPs        int64    `json:"timeMinPs"`
	TimeMaxPs        int64    `json:"timeMaxPs"`
	EdgeHash         string   `json:"edgeHash"`
}

type BudgetReport struct {
	MaxCandidates int `json:"maxCandidates"`
	Used          int `json:"used"`
}

type Generation struct {
	ID          string         `json:"id"`
	CaptureID   string         `json:"captureId"`
	ParamsIDs   map[string]string `json:"paramsIds"` // bus -> params id
	ParentGenID string         `json:"parentGenId,omitempty"`
	Kind        string         `json:"kind"` // full | patch
	Trigger     string         `json:"trigger,omitempty"`
	Status      string         `json:"status"`
	Summary     InputSummary   `json:"inputSummary"`
	Budget      BudgetReport   `json:"budget"`
	Transactions []*Transaction `json:"transactions"`
	Diagnostics []*Diagnostic  `json:"diagnostics,omitempty"`
	DecoderVersion string       `json:"decoderVersion"`
	CreatedAt   string         `json:"createdAt"`
}

// Event is the machine-readable ledger record. Payload is a typed struct
// (marshaled to JSON in storage); Parents carry provenance edges.
type Event struct {
	ID          string          `json:"id"`
	Ordinal     int             `json:"ordinal"`
	Type        string          `json:"type"`
	Branch      string          `json:"branch"`
	Subject     string          `json:"subject"`
	Source      string          `json:"source"` // seed | import | decode | manual | archive | system
	At          string          `json:"at"`
	GenID       string          `json:"genId,omitempty"`
	ParamsID    string          `json:"paramsId,omitempty"`
	Parents     []string        `json:"parents"`
	InputDigest string          `json:"inputDigest,omitempty"`
	Conclusion  string          `json:"conclusion"`
	Payload     json.RawMessage `json:"payload"`
}

type Decision struct {
	ID            string `json:"id"`
	Branch        string `json:"branch"`
	Action        string `json:"action"` // ACCEPT | REJECT | ADJUST
	TargetObject  string `json:"targetObject"`
	CandidateKey  string `json:"candidateKey,omitempty"`
	NewParamsID   string `json:"newParamsId,omitempty"`
	BasedOnGen    string `json:"basedOnGen"`
	ParentDecision string `json:"parentDecision,omitempty"`
	Note          string `json:"note"`
	By            string `json:"by"`
	CreatedAt     string `json:"createdAt"`
}

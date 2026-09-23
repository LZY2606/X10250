package decode

import (
	"edgebench/internal/edge"
)

// BusKind enumerates the decoders that ship in the case package.
type BusKind string

const (
	BusI2C    BusKind = "i2c"
	BusSPI    BusKind = "spi"
	BusCustom BusKind = "custom"
)

// Edge is the derived-layer view of a raw edge: normalized time and polarity
// inversion already applied, while raw fields remain available.
type Edge struct {
	ID        string  `json:"id"`
	SourceID  string  `json:"sourceId"`
	Channel   string  `json:"channel"`
	Alias     string  `json:"alias"`
	T         float64 `json:"tNs"`
	RawLevel  string  `json:"rawLevel"`
	Level     string  `json:"level"` // after inversion
	Inverted  bool    `json:"inverted"`
	RawTime   string  `json:"rawTime"`
	Order     int     `json:"order"`
}

// CSLine describes one SPI device-select line.
type CSLine struct {
	Channel   string `json:"channel"`
	ActiveLow bool   `json:"activeLow"`
	Device    string `json:"device"`
}

// WireConfig is the decoder parameter set for one bus.  Versioning is handled
// by the caller (parameter set id + decoder version).
type WireConfig struct {
	Key          string   `json:"key"`
	Kind         BusKind  `json:"kind"`
	SCL          string   `json:"scl,omitempty"`
	SDA          string   `json:"sda,omitempty"`
	SCLK         string   `json:"sclk,omitempty"`
	MOSI         string   `json:"mosi,omitempty"`
	MISO         string   `json:"miso,omitempty"`
	Clock        string   `json:"clock,omitempty"` // custom
	Data         string   `json:"data,omitempty"`  // custom
	CS           []CSLine `json:"cs,omitempty"`
	CPOL         int      `json:"cpol,omitempty"` // -1 = unknown
	CPHA         int      `json:"cpha,omitempty"` // -1 = unknown
	SampleEdge   string   `json:"sampleEdge,omitempty"` // custom: rise|fall|unknown
	StretchRatio float64  `json:"stretchRatio,omitempty"`
	GapRatio     float64  `json:"gapRatio,omitempty"`
	GuardRatio   float64  `json:"guardRatio,omitempty"`
}

// DecoderVersion identifies the rule implementation that produced a result.
const DecoderVersion = "edgebench-decode-2026.09"

// Structured decoder evidence events (machine readable).
const (
	EvStart            = "start"
	EvRepeatedStart    = "repeated_start"
	EvStop             = "stop"
	EvClockStretch     = "clock_stretch"
	EvMissingClockEdge = "missing_clock_edge"
	EvDataGap          = "data_gap"
	EvBoundarySample   = "boundary_sample"
	EvCandidateTie     = "candidate_tie"
	EvCSActivate       = "cs_activate"
	EvCSDeactivate     = "cs_deactivate"
	EvCSOverlap        = "cs_overlap"
	EvPartialByte      = "partial_byte"
)

// EvidenceEvent is a structured, replayable piece of decoder evidence.
type EvidenceEvent struct {
	Type    string   `json:"type"`
	T       float64  `json:"tNs"`
	T2      float64  `json:"t2Ns,omitempty"`
	Channel string   `json:"channel,omitempty"`
	EdgeIDs []string `json:"edgeIds,omitempty"`
	Message string   `json:"message,omitempty"`
	ByteKey string   `json:"byteKey,omitempty"`
	FrameKey string  `json:"frameKey,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// Candidate is one legal byte interpretation.
type Candidate struct {
	Key        string   `json:"key"`
	Value      int      `json:"value"`
	Hex        string   `json:"hex"`
	Confidence float64  `json:"confidence"`
	Basis      string   `json:"basis"`
	EdgeIDs    []string `json:"edgeIds,omitempty"`
}

// Byte is one aligned byte slot (or an incomplete tail) on a track.
type Byte struct {
	Key           string       `json:"key"`
	Track         string       `json:"track"` // i2c: "sda", spi: "mosi"/"miso", custom: "data"
	Index         int          `json:"index"`
	Value         int          `json:"value"` // -1 when unresolved
	Hex           string       `json:"hex"`
	Complete      bool         `json:"complete"`
	Candidates    []Candidate  `json:"candidates"`
	ACK           string       `json:"ack,omitempty"` // ack|nack|unknown
	AckEdgeIDs    []string     `json:"ackEdgeIds,omitempty"`
	TStart        float64      `json:"tStartNs"`
	TEnd          float64      `json:"tEndNs"`
	SampleEdgeIDs []string     `json:"sampleEdgeIds"`
	Note          string       `json:"note,omitempty"`
}

// Frame groups bytes on a bus.  Valid=false frames are retained, never dropped.
type Frame struct {
	Key          string          `json:"key"`
	Kind         BusKind         `json:"kind"`
	Index        int             `json:"index"`
	Device       string          `json:"device,omitempty"`
	Direction    string          `json:"direction,omitempty"`
	TStart       float64         `json:"tStartNs"`
	TEnd         float64         `json:"tEndNs"`
	Bytes        []*Byte         `json:"bytes"`
	Events       []EvidenceEvent `json:"events"`
	StartEdgeIDs []string        `json:"startEdgeIds,omitempty"`
	StopEdgeIDs  []string        `json:"stopEdgeIds,omitempty"`
	Valid        bool            `json:"valid"`
	Reason       string          `json:"reason,omitempty"`
}

// Result is the full decoder output for one bus.
type Result struct {
	BusKey        string             `json:"busKey"`
	Kind          BusKind            `json:"kind"`
	DecoderVersion string            `json:"decoderVersion"`
	Frames        []*Frame           `json:"frames"`
	Events        []EvidenceEvent   `json:"events"`
	Edges         []Edge             `json:"edges"`
	Diagnostics   []edge.Diagnostic  `json:"diagnostics"`
	Complete      bool               `json:"complete"` // false if a budget stopped decoding early
	InputSummary  InputSummary       `json:"inputSummary"`
}

// InputSummary is recorded with every derived result so recomputation can be
// provenance-checked.
type InputSummary struct {
	SourceIDs   []string `json:"sourceIds"`
	EdgeCount   int      `json:"edgeCount"`
	Channels    []string `json:"channels"`
	SampleRateHz float64 `json:"sampleRateHz"`
	SampleRateRaw string `json:"sampleRateRaw"`
	ParamSetID  string   `json:"paramSetId"`
	ParamVersion int     `json:"paramVersion"`
	DecoderVersion string `json:"decoderVersion"`
}

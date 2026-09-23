package domain

// ParamSet is an immutable, revisioned parameter version. Revising any value
// (clock polarity, boundary-sample rule, decoder version...) creates a new
// ParamSet with a new id and a pointer to the parent revision; material is not
// touched and dependent runs can be recomputed without invalidating history.
type ParamSet struct {
	ID              string  `json:"id"`
	ParentID        string  `json:"parentId,omitempty"`
	Revision        int     `json:"revision"`
	DecoderKind     string  `json:"decoderKind"`     // i2c | spi | sync
	DecoderVersion  string  `json:"decoderVersion"`  // e.g. dec-i2c/v1.0.0
	ClockPolarity   string  `json:"clockPolarity"`   // high | low (SPI CPOL hint)
	ClockPhase      string  `json:"clockPhase"`      // first | second (SPI CPHA hint)
	SampleOn        string  `json:"sampleOn"`        // first-edge | second-edge (sync/spi derived rule)
	BitOrder        string  `json:"bitOrder"`        // msb-first | lsb-first
	WordBits        int     `json:"wordBits"`
	BoundaryMode    string  `json:"boundaryMode"`    // strict | tolerant (sample near bit boundary)
	RepeatedStart   string  `json:"repeatedStart"`   // link | split
	StretchPolicy   string  `json:"stretchPolicy"`   // wait | truncate
	GapThreshold    Quantity `json:"gapThreshold"`
	CSActive        string  `json:"csActive"`        // low | high
	MaxCandidates   int     `json:"maxCandidates"`
	Note            string  `json:"note,omitempty"`
}

// InputSummary is the deterministic digest of what a run consumed.
type InputSummary struct {
	CaptureID string `json:"captureId"`
	EdgeCount int    `json:"edgeCount"`
	FirstT    string `json:"firstT"`
	LastT     string `json:"lastT"`
	Sha256    string `json:"sha256"`
}

// Byte is one decoded data unit with its evidence window. EdgeFrom/EdgeTo point
// at channel/edge indexes in the material so any displayed byte can return to
// the exact edges.
type Byte struct {
	ID        string  `json:"id"`
	Index     int     `json:"index"`
	Value     *int    `json:"value"` // nil => not uniquely determined
	ValueText string  `json:"valueText"`
	Kind      string  `json:"kind"`  // addr | data | ack | nack | unknown
	Ack       string  `json:"ack,omitempty"` // ack | nack | ""
	TStart    string  `json:"tStart"`
	TEnd      string  `json:"tEnd"`
	EdgeRefs  []EdgeRef `json:"edgeRefs"`
	Flags     []string `json:"flags,omitempty"`
}

// EdgeRef is a stable pointer into material: channel id + 0-based edge index.
type EdgeRef struct {
	Channel string `json:"channel"`
	Index   int    `json:"index"`
}

// Candidate is one legal decode possibility for an ambiguous region.
type Candidate struct {
	ID          string   `json:"id"`
	Values      []string `json:"values"`
	Confidence  float64  `json:"confidence"`
	Reason      string   `json:"reason"`
	Accepted    *bool    `json:"accepted,omitempty"` // nil = undecided
}

// CandidateGroup holds every legal candidate for one ambiguous window.
type CandidateGroup struct {
	ID       string      `json:"id"`
	ByteID   string      `json:"byteId,omitempty"`
	Reason   string      `json:"reason"`
	Code     string      `json:"code"` // missing-clock-edge | spi-mode-unknown | boundary-sample | ambiguous-bit
	Items    []Candidate `json:"items"`
}

// Transaction is a reconstructed bus transaction. Data gaps never stitch two
// fragments together: a fragment ends the transaction with gap-terminated.
type Transaction struct {
	ID            string           `json:"id"`
	Index         int              `json:"index"`
	Bus           string           `json:"bus"`
	Device        string           `json:"device,omitempty"`
	StartT        string           `json:"startT"`
	EndT          string           `json:"endT"`
	Bytes         []Byte           `json:"bytes"`
	RepeatedStart bool             `json:"repeatedStart,omitempty"`
	Stretched     []StretchSpan    `json:"stretched,omitempty"`
	Truncated     bool             `json:"truncated,omitempty"`
	GapTerminated bool             `json:"gapTerminated,omitempty"`
	GapFrom       string           `json:"gapFrom,omitempty"`
	Groups        []CandidateGroup `json:"groups,omitempty"`
	Flags         []string         `json:"flags,omitempty"`
}

// StretchSpan records one clock-stretch interval (clock held by a device).
type StretchSpan struct {
	From string  `json:"from"`
	To   string  `json:"to"`
	Who  string  `json:"who"`
	Us   float64 `json:"us"`
}

// DecodeRun is the derived result of decoding one capture under one ParamSet.
type DecodeRun struct {
	ID           string        `json:"id"`
	CaptureID    string        `json:"captureId"`
	ParamID      string        `json:"paramId"`
	ParentRunID  string        `json:"parentRunId,omitempty"`
	Decoder      string        `json:"decoder"`
	Status       string        `json:"status"` // ok | budget-exhausted | corrupt | semantically-unsolvable
	Input        InputSummary  `json:"input"`
	Transactions []Transaction `json:"transactions"`
	Diagnostics  []Diagnostic  `json:"diagnostics,omitempty"`
	Groups       []CandidateGroup `json:"groups,omitempty"`
}

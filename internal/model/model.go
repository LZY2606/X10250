// Package model defines the immutable evidence layer and the derived decision
// layer of the edge decoding bench.
//
// Layering rule:
//   - immutable material: raw edge time tokens, sample rate, channel aliases,
//     level inversion, clock polarity and decoder version are never rewritten;
//   - derived material: normalized quantities, decode results, candidates and
//     human decisions. Every derived object records an input digest, a
//     parameter version and parent dependencies.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// DecoderVersion is the rule-set version stamped on every derived result.
// Bumping it never recomputes history: old results keep the version they were
// produced with.
const DecoderVersion = "edge-decoder-2026.09.0"

// Result classes required by the specification. They are deliberately three
// distinct outcomes so callers cannot confuse a corrupt file, an exhausted
// budget and a signal that has no legal interpretation.
const (
	ClassOK      = "ok"
	ClassCorrupt = "format-corrupt"
	ClassBudget  = "budget-exhausted"
	ClassNoSeman = "semantic-unsolvable"
)

// Diagnostic is a located finding. Positions always point back at concrete
// evidence (channel alias + edge sequence + raw time token) whenever the
// finding touches an edge.
type Diagnostic struct {
	Code     string    `json:"code"`
	Severity string    `json:"severity"` // error | warning | info
	Message  string    `json:"message"`
	Position *Position `json:"position,omitempty"`
}

// Position is a coordinate in the imported evidence.
type Position struct {
	Source  string `json:"source,omitempty"`
	Line    int    `json:"line,omitempty"`
	Channel string `json:"channel,omitempty"`
	EdgeSeq int    `json:"edgeSeq,omitempty"`
	EdgeID  string `json:"edgeId,omitempty"`
	WhenRaw string `json:"whenRaw,omitempty"`
	Byte    int    `json:"byte,omitempty"`
	Bit     int    `json:"bit,omitempty"`
}

// Quantity preserves the original textual representation of a domain quantity
// and adds a normalized value only when conversion is legal. A value that
// cannot be converted stays in place (Raw) with Valid=false and a diagnostic;
// it is never dropped to make a plot draw.
type Quantity struct {
	Raw      string      `json:"raw"`
	Value    float64     `json:"value,omitempty"`
	Unit     string      `json:"unit,omitempty"`
	BaseUnit string      `json:"baseUnit,omitempty"`
	Valid    bool        `json:"valid"`
	Diagnosis *Diagnostic `json:"diagnosis,omitempty"`
}

// Edge is one raw digital transition as imported. Level is the physical level
// before alias inversion; effective levels are a derived concern.
type Edge struct {
	ID      string   `json:"id"`
	Channel string   `json:"channel"`
	When    Quantity `json:"when"`
	Level   int      `json:"level"`
	Seq     int      `json:"seq"`
}

// Probe is the probe/channel configuration of a capture.
type Probe struct {
	Channel  string `json:"channel"`
	Alias    string `json:"alias"`
	Role     string `json:"role"` // SCL SDA SCK MOSI MISO CS SYNC SYNCCLK
	Inverted bool   `json:"inverted"`
}

// Capture is the immutable import unit.
type Capture struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // i2c | spi | sync
	SourceName string   `json:"sourceName"`
	SampleRate Quantity `json:"sampleRate"`
	TimeBase   string   `json:"timeBase"`
	Probes     []Probe  `json:"probes"`
	Edges      []Edge   `json:"edges"`
	ImportedAt string   `json:"importedAt"`
	DecoderHintVersion string `json:"decoderHintVersion"`
}

// Params is a versioned, immutable parameter set. A revision creates a new
// Params with ParentID set; nil pointer fields mean "unknown", which forces
// candidate retention instead of a silent default.
type Params struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	ParentID       string `json:"parentId,omitempty"`
	CPOL           *int   `json:"cpol,omitempty"`
	CPHA           *int   `json:"cpha,omitempty"`
	BoundaryWindowS     float64 `json:"boundaryWindowS,omitempty"`
	MaxGapS        float64 `json:"maxGapS,omitempty"`
	DecoderVersion string `json:"decoderVersion"`
	Note           string `json:"note,omitempty"`
	CreatedAt      string `json:"createdAt"`
}

// Byte is a decoded byte with edge-level provenance. Every listed edge id can
// be walked back to the raw capture.
type Byte struct {
	Key          string   `json:"key"`
	Index        int      `json:"index"`
	Value        int      `json:"value"`
	Hex          string   `json:"hex"`
	ACK          *bool    `json:"ack,omitempty"`
	BitValues    []int    `json:"bitValues"`
	ClockEdgeIDs []string `json:"clockEdgeIds"`
	DataEdgeIDs  []string `json:"dataEdgeIds"`
	StartEdgeID  string   `json:"startEdgeId,omitempty"`
	EndEdgeID    string   `json:"endEdgeId,omitempty"`
	Status       string   `json:"status"`
	Notes        []string `json:"notes,omitempty"`
}

// Candidate is one legal decoding of one transaction. When the evidence does
// not determine a unique answer, all legal candidates are retained.
type Candidate struct {
	Key        string  `json:"key"`
	Protocol   string  `json:"protocol"`
	Title      string  `json:"title"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	CPOL       *int    `json:"cpol,omitempty"`
	CPHA       *int    `json:"cpha,omitempty"`
	BitOrder   string  `json:"bitOrder,omitempty"`
	Bytes      []Byte  `json:"bytes"`
}

// Transaction is a reconstructed bus transaction (or an unmerged fragment).
type Transaction struct {
	ID          string       `json:"id"`
	Index       int          `json:"index"`
	Kind        string       `json:"kind"`
	ChipSelect  string       `json:"chipSelect,omitempty"`
	StartEdgeID string       `json:"startEdgeId"`
	EndEdgeID   string       `json:"endEdgeId"`
	Markers     []string     `json:"markers"` // repeated-start clock-stretching data-gap boundary-sample missing-clock-edge multi-cs fragment
	Candidates  []Candidate  `json:"candidates"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Fragment    bool         `json:"fragment"`
}

// DecodeResult is a derived, versioned decoding of one capture under one
// parameter version.
type DecodeResult struct {
	ID             string        `json:"id"`
	Kind           string        `json:"kind"`
	CaptureID      string        `json:"captureId"`
	ParamID        string        `json:"paramId"`
	ParentResultID string        `json:"parentResultId,omitempty"`
	Tag            string        `json:"tag,omitempty"` // baseline | cpol-flip | ...
	DecoderVersion string        `json:"decoderVersion"`
	InputDigest    string        `json:"inputDigest"`
	Class          string        `json:"class"`
	Transactions   []Transaction `json:"transactions"`
	Diagnostics    []Diagnostic  `json:"diagnostics,omitempty"`
	Accepted       bool          `json:"accepted"`
	CreatedAt      string        `json:"createdAt"`
	EventSeq       int           `json:"eventSeq"`
}

// Decision is a human verdict on one transaction of one result. Conflicting
// decisions are both retained; the state projection marks conflicts instead
// of overwriting the earlier one.
type Decision struct {
	ID               string            `json:"id"`
	ResultID         string            `json:"resultId"`
	TransactionIndex int               `json:"transactionIndex"`
	CandidateKey     string            `json:"candidateKey"`
	Verdict          string            `json:"verdict"` // accept | reject | adjust
	ParamPatch       map[string]any    `json:"paramPatch,omitempty"`
	Rationale        string            `json:"rationale"`
	At               string            `json:"at"`
	EventSeq         int               `json:"eventSeq"`
	Supersedes       string            `json:"supersedes,omitempty"`
}

// Event is the append-only, exportable record of everything that happened.
type Event struct {
	Seq       int            `json:"seq"`
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Branch    string         `json:"branch"`
	At        string         `json:"at"`
	Parents   []string       `json:"parents"`
	Summary   string         `json:"summary"`
	Payload   map[string]any `json:"payload"`
}

// Study is the counter-example archive: one baseline plus scoped perturbations.
type Study struct {
	Name            string   `json:"name"`
	BaselineResult  string   `json:"baselineResult"`
	Results         []string `json:"results"`
	Description     string   `json:"description"`
}

// Digest returns a stable content identity. Canonical JSON (Go sorts map keys)
// makes equal inputs produce equal ids across export/import.
func Digest(parts ...any) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, p := range parts {
		_ = enc.Encode(p)
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))[:16]
}

// ShortID returns a stable id built from a labeled digest.
func ShortID(label string, parts ...any) string {
	sum := Digest(parts...)
	return label + "_" + sum[7:]
}

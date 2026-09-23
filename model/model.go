// Package model defines the domain types for the edge-decoding workbench:
// immutable evidence (captures, probe configs, decoder versions) and the
// derived, revisable decode runs with their candidates and diagnostics.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// DecoderVersion identifies the rule set that produced a decode run.
// Bumping it creates a new parameter/rule version; old runs keep theirs.
const DecoderVersion = "dec-1.3.0"

// Failure classes: resource budget, corrupt format and semantic dead-ends
// are three distinct results, never collapsed into one generic error.
var (
	ErrBudgetExhausted = errors.New("resource budget exhausted")
	ErrCorruptFormat   = errors.New("corrupt format")
	ErrUnsatisfiable   = errors.New("semantically unsatisfiable")
)

// Failure classifies an error into one of the three distinct outcomes.
func Failure(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrBudgetExhausted):
		return "budget_exhausted"
	case errors.Is(err, ErrCorruptFormat):
		return "corrupt_format"
	case errors.Is(err, ErrUnsatisfiable):
		return "unsatisfiable"
	default:
		return "internal"
	}
}

// Diag is a positioned diagnostic. Diagnostics never carry empty/default
// values disguised as success; they point at the exact input location.
type Diag struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // info | warning | error
	Path     string `json:"path"`     // JSON-ish path into the input, e.g. channels[1].edges[3]
	Message  string `json:"message"`
	AtNs     *int64 `json:"atNs,omitempty"`
}

// RawValue preserves the original textual representation of a domain
// quantity. Normalization happens only in the derived layer; values that
// cannot be converted stay in place with a diagnostic.
type RawValue struct {
	Raw  string `json:"raw"`
	Unit string `json:"unit"` // ns | us | ms | s | Hz | kHz | MHz | "" (unitless)
}

// TimeValue is a normalized time in nanoseconds plus its source form.
type TimeValue struct {
	Ns  int64  `json:"ns"`
	Raw string `json:"raw"`
}

// Edge is one digital transition on a channel. Level is the logical level
// AFTER the edge, already corrected by the channel's invert flag; the raw
// captured level is kept alongside.
type Edge struct {
	Channel  string    `json:"channel"`
	Time     TimeValue `json:"time"`
	Level    int       `json:"level"`    // 0/1 after inversion correction
	RawLevel int       `json:"rawLevel"` // as captured
	CapIndex  int      `json:"capIndex"`
	OrigIndex int      `json:"origIndex"`
	Unconvertible bool `json:"unconvertible,omitempty"`
}

// EdgeRef is a stable pointer to an edge inside a capture fragment.
func EdgeRef(captureID string, index int) string {
	return fmt.Sprintf("%s#e%d", captureID, index)
}

// ChannelProbe describes one probed digital channel.
type ChannelProbe struct {
	Name     string `json:"name"`     // physical channel, e.g. "D0"
	Alias    string `json:"alias"`    // reviewer-facing alias, e.g. "SDA"
	Inverted bool   `json:"inverted"` // electrical level inversion flag (preserved verbatim)
	Role     string `json:"role"`     // sda|scl|clk|mosi|miso|cs|frame|data
}

// ProbeConfig is the immutable probe/decoder configuration of a capture.
type ProbeConfig struct {
	SampleRateHz  float64        `json:"sampleRateHz"`
	ClockPolarity string         `json:"clockPolarity"` // rise|fall (sample edge)
	Channels      []ChannelProbe `json:"channels"`
}

// Capture is the immutable imported evidence: raw edges + probe config.
type Capture struct {
	ID           string      `json:"id"`
	Label        string      `json:"label"`
	ImportedAt   string      `json:"importedAt"`
	InitialLevel map[string]int `json:"initialLevel"` // logical level per channel at window start
	Edges        []Edge      `json:"edges"`
	Probe        ProbeConfig `json:"probe"`
	Diags        []Diag      `json:"diags"` // ingest diagnostics (kept with the evidence)
	SourceRaw    string      `json:"sourceRaw,omitempty"`
}

// DecodeParams are the revisable parameters of a decode run.
type DecodeParams struct {
	Protocol        string  `json:"protocol"` // i2c | spi | sync
	ClockPolarity   string  `json:"clockPolarity"`
	BitOrder        string  `json:"bitOrder"` // msb | lsb
	SPIMode         int     `json:"spiMode"`  // -1 = unknown -> keep all mode candidates
	StretchThreshNs int64   `json:"stretchThreshNs"`
	GapThreshNs     int64   `json:"gapThreshNs"` // gaps longer than this must not be stitched
	BoundaryTolNs   int64   `json:"boundaryTolNs"`
	Budget          int     `json:"budget"` // max merged edge events processed
}

// DefaultParams returns the baseline parameter set for a protocol.
func DefaultParams(protocol string, p ProbeConfig) DecodeParams {
	pol := p.ClockPolarity
	if pol == "" {
		pol = "rise"
	}
	return DecodeParams{
		Protocol:        protocol,
		ClockPolarity:   pol,
		BitOrder:        "msb",
		SPIMode:         -1,
		StretchThreshNs: 2000,
		GapThreshNs:     100000,
		BoundaryTolNs:   30,
		Budget:          100000,
	}
}

// ByteCandidate is one admissible decoding of a byte position.
type ByteCandidate struct {
	Value      int      `json:"value"`
	BitLevels  []int    `json:"bitLevels"`
	SampleAtNs []int64  `json:"sampleAtNs"`
	Confidence float64  `json:"confidence"`
	Basis      []string `json:"basis"` // machine-readable judgement basis codes
}

// Byte is one decoded byte (or would-be byte) with ACK/NACK info and
// per-byte candidates when the decoding is not unique.
type Byte struct {
	ID          string          `json:"id"`
	Index       int             `json:"index"`
	Value       int             `json:"value"`
	BitLevels   []int           `json:"bitLevels"`
	SampleAtNs  []int64         `json:"sampleAtNs"`
	EdgeRefs    []string        `json:"edgeRefs"`
	Ack         *bool           `json:"ack,omitempty"` // true=ACK false=NACK (I2C)
	StartNs     int64           `json:"startNs"`
	EndNs       int64           `json:"endNs"`
	Candidates  []ByteCandidate `json:"candidates,omitempty"`
	ValidFromNs int64           `json:"validFromNs"`
	ValidToNs   int64           `json:"validToNs"`
	InvalidWhy  string          `json:"invalidWhy,omitempty"`
}

// Transaction is one bus transaction inside an alternative.
type Transaction struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"` // i2c|spi|sync
	Label      string   `json:"label"`
	StartNs    int64    `json:"startNs"`
	EndNs      int64    `json:"endNs"`
	GapBroken  bool     `json:"gapBroken"` // ended by a data gap, not a clean STOP/CS
	CSAlias    string   `json:"csAlias,omitempty"`
	Device     string   `json:"device,omitempty"`
	Address    int      `json:"address,omitempty"`
	Read       bool     `json:"read,omitempty"`
	Bytes      []Byte   `json:"bytes"`
	Repeated   bool     `json:"repeated,omitempty"`   // contains repeated START
	Stretched  bool     `json:"stretched,omitempty"`  // clock stretching observed
	InvalidWhy string   `json:"invalidWhy,omitempty"`
}

// Alternative is one admissible decoding of the whole fragment.
type Alternative struct {
	ID           string        `json:"id"`
	Label        string        `json:"label"`
	Confidence   float64       `json:"confidence"`
	Selected     bool          `json:"selected"`
	Basis        []string      `json:"basis"`
	Transactions []Transaction `json:"transactions"`
}

// Run is a derived decode result. It records input digest, parameter
// version and parent dependencies; it never mutates its inputs.
type Run struct {
	ID            string        `json:"id"`
	CaptureID     string        `json:"captureId"`
	FragmentID    string        `json:"fragmentId"`
	Params        DecodeParams  `json:"params"`
	DecoderVer    string        `json:"decoderVersion"`
	InputDigest   string        `json:"inputDigest"`
	ParentRunID   string        `json:"parentRunId,omitempty"`
	Alternatives  []Alternative `json:"alternatives"`
	Diags         []Diag        `json:"diags"`
	ValidFromNs   int64         `json:"validFromNs"`
	ValidToNs     int64         `json:"validToNs"`
	Truncated     bool          `json:"truncated"`
	CreatedByEvent string       `json:"createdByEvent"`
}

// Selected returns the currently selected alternative.
func (r *Run) Selected() *Alternative {
	for i := range r.Alternatives {
		if r.Alternatives[i].Selected {
			return &r.Alternatives[i]
		}
	}
	if len(r.Alternatives) > 0 {
		return &r.Alternatives[0]
	}
	return nil
}

// FindByte locates a byte by stable id across alternatives.
func (r *Run) FindByte(byteID string) (*Alternative, *Transaction, *Byte) {
	for ai := range r.Alternatives {
		a := &r.Alternatives[ai]
		for ti := range a.Transactions {
			t := &a.Transactions[ti]
			for bi := range t.Bytes {
				if t.Bytes[bi].ID == byteID {
					return a, t, &t.Bytes[bi]
				}
			}
		}
	}
	return nil, nil, nil
}

// ---- Event model -------------------------------------------------------

// Event types (append-only journal; every reviewer action is an event).
const (
	EvCaptureImported  = "capture.imported"
	EvDecodeRun        = "decode.run.created"
	EvDecisionAsserted = "decision.asserted"
	EvAltSelected      = "alternative.selected"
	EvRationale        = "rationale.recorded"
	EvArchiveEntry     = "archive.entry.recorded"
	EvImportRecovered  = "import.recovered"
)

// Event is one journal entry. IDs are content-addressed over
// (seq, prevID, type, body) so export/import round-trips keep identity.
type Event struct {
	ID   string          `json:"id"`
	Seq  int64           `json:"seq"`
	Prev string          `json:"prev"`
	Type string          `json:"type"`
	At   string          `json:"at"`
	Body json.RawMessage `json:"body"`
}

// EventID computes the stable identity of an event.
func EventID(seq int64, prev, typ string, body json.RawMessage) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%d|%s|%s|", seq, prev, typ)))
	h.Write(body)
	return "ev-" + hex.EncodeToString(h.Sum(nil))[:24]
}

// ---- Stable identities -------------------------------------------------

// CaptureID derives the stable capture identity from its content.
func CaptureID(label string, probe ProbeConfig, edges []Edge) string {
	h := sha256.New()
	h.Write([]byte(label))
	b, _ := json.Marshal(probe)
	h.Write(b)
	for _, e := range edges {
		fmt.Fprintf(h, "%s/%d/%d;", e.Channel, e.Time.Ns, e.Level)
	}
	return "cap-" + hex.EncodeToString(h.Sum(nil))[:16]
}

// FragmentID derives the stable fragment identity.
func FragmentID(captureID string, fromNs, toNs int64) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%d", captureID, fromNs, toNs)))
	return "frag-" + hex.EncodeToString(s[:])[:16]
}

// RunID derives the stable run identity from inputs+params+rule version.
func RunID(fragmentID string, p DecodeParams, decoderVer string) string {
	b, _ := json.Marshal(p)
	s := sha256.Sum256([]byte(fragmentID + "|" + string(b) + "|" + decoderVer))
	return "run-" + hex.EncodeToString(s[:])[:16]
}

// InputDigest summarizes the exact decode inputs for audit.
func InputDigest(capture *Capture, fromNs, toNs int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s|%d|%d|%s|%v|%s|%d|", capture.ID, fromNs, toNs,
		capture.Probe.ClockPolarity, capture.Probe.SampleRateHz, DecoderVersion, len(capture.Edges))
	for _, ch := range capture.Probe.Channels {
		fmt.Fprintf(h, "%s=%s/%v/%s;", ch.Name, ch.Alias, ch.Inverted, ch.Role)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// AltID / TxID / ByteID are stable, content-derived identities.
func AltID(runID, label string) string {
	s := sha256.Sum256([]byte(runID + "|alt|" + label))
	return "alt-" + hex.EncodeToString(s[:])[:12]
}

func TxID(altID string, index int, startNs int64) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s|tx|%d|%d", altID, index, startNs)))
	return "tx-" + hex.EncodeToString(s[:])[:12]
}

func ByteID(txID string, index int, startNs int64) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s|byte|%d|%d", txID, index, startNs)))
	return "byte-" + hex.EncodeToString(s[:])[:12]
}

// DecisionID identifies a reviewer decision event target.
func DecisionID(byteID string, seq int64) string {
	s := sha256.Sum256([]byte(fmt.Sprintf("%s|dec|%d", byteID, seq)))
	return "dec-" + hex.EncodeToString(s[:])[:12]
}

// SortByteCandidates orders candidates deterministically so equivalent
// inputs always yield a stable order.
func SortByteCandidates(cs []ByteCandidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		if cs[i].Value != cs[j].Value {
			return cs[i].Value < cs[j].Value
		}
		return strings.Join(cs[i].Basis, ",") < strings.Join(cs[j].Basis, ",")
	})
}

// SortAlternatives orders alternatives deterministically (stable order for
// equivalent inputs).
func SortAlternatives(as []Alternative) {
	sort.SliceStable(as, func(i, j int) bool {
		if as[i].Confidence != as[j].Confidence {
			return as[i].Confidence > as[j].Confidence
		}
		return as[i].Label < as[j].Label
	})
}

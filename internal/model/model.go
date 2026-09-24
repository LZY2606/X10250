// Package model defines the domain types for the edge-decoding workbench.
// Layers:
//   - Immutable materials: Capture (raw edges), ProbeConfig, Params (versioned).
//   - Derived results: RunRecord (decode output), content-addressed by input digest.
//   - Mutable decisions: append-only events referencing stable IDs.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// HashID derives a stable, content-addressed identity. Equivalent inputs
// always produce the same ID, which keeps identity stable across
// export / delete / re-import round trips.
func HashID(prefix string, parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(h.Sum(nil))[:16])
}

// Edge is a raw digital transition. Sample is the original representation
// (sample index into the capture); nanoseconds are a derived-layer
// normalization computed via the capture sample rate.
type Edge struct {
	ID      string `json:"id"`
	Channel string `json:"channel"` // source channel name on the probe
	Sample  int64  `json:"sample"`
	Level   int    `json:"level"` // raw level after the edge (0/1), before inversion
}

// Gap marks a capture discontinuity. Bytes on either side of a gap must
// never be spliced into one transaction.
type Gap struct {
	ID     string `json:"id"`
	Sample int64  `json:"sample"`
	Reason string `json:"reason"`
}

// Capture is an immutable material: raw edges + sample rate + gaps.
type Capture struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	SampleRateHz float64  `json:"sample_rate_hz"`
	Channels     []string `json:"channels"`
	Edges        []Edge   `json:"edges"`
	Gaps         []Gap    `json:"gaps"`
}

// NewCapture builds a capture with deterministic edge IDs (content-based,
// stable under reordering and localized edits).
func NewCapture(label string, rate float64, channels []string, edges []Edge, gaps []Gap) *Capture {
	c := &Capture{Label: label, SampleRateHz: rate, Channels: channels, Edges: edges, Gaps: gaps}
	for i := range c.Edges {
		e := &c.Edges[i]
		e.ID = HashID("edg", label, e.Channel, e.Sample, e.Level)
	}
	for i := range c.Gaps {
		g := &c.Gaps[i]
		g.ID = HashID("gap", label, g.Sample, g.Reason)
	}
	c.ID = HashID("cap", label, rate, channels, edges, gaps)
	return c
}

// TimeNs converts a sample index to nanoseconds from capture start.
// ok=false means the value is unconvertible (missing sample rate); callers
// must keep the original sample value and attach a diagnostic.
func (c *Capture) TimeNs(sample int64) (ns float64, ok bool) {
	if c.SampleRateHz <= 0 {
		return 0, false
	}
	return float64(sample) / c.SampleRateHz * 1e9, true
}

// ChannelMap binds a probe source channel to a bus alias. Invert records
// hardware level inversion; it is preserved as material and applied only in
// the derived layer.
type ChannelMap struct {
	Alias  string `json:"alias"`
	Source string `json:"source"`
	Role   string `json:"role"` // sda|scl|sclk|mosi|miso|cs|data|clk ("" = infer)
	Invert bool   `json:"invert"`
}

// ProbeConfig is an immutable, versioned material.
type ProbeConfig struct {
	ID       string       `json:"id"`
	Version  int          `json:"version"`
	Channels []ChannelMap `json:"channels"`
}

func NewProbeConfig(version int, channels []ChannelMap) *ProbeConfig {
	c := &ProbeConfig{Version: version, Channels: channels}
	c.ID = HashID("prb", version, channels)
	return c
}

// Params is an immutable, versioned decoder-parameter material.
type Params struct {
	ID             string   `json:"id"`
	Version        int      `json:"version"`
	DecoderVersion string   `json:"decoder_version"`
	ClockPolarity  string   `json:"clock_polarity"` // rising | falling
	SampleOffsetNs float64  `json:"sample_offset_ns"`
	SPIMode        string   `json:"spi_mode"` // unknown | 0 | 1 | 2 | 3
	StretchMinNs   float64  `json:"stretch_min_ns"`
	Budget         int      `json:"budget"`
	Protocols      []string `json:"protocols"`
}

func NewParams(version int, decoderVersion, polarity string, offsetNs float64, spiMode string, stretchMinNs float64, budget int, protocols []string) *Params {
	p := &Params{
		Version: version, DecoderVersion: decoderVersion, ClockPolarity: polarity,
		SampleOffsetNs: offsetNs, SPIMode: spiMode, StretchMinNs: stretchMinNs,
		Budget: budget, Protocols: protocols,
	}
	p.ID = HashID("par", version, decoderVersion, polarity, offsetNs, spiMode, stretchMinNs, budget, protocols)
	return p
}

// Diagnostic is a positioned, machine-readable problem report. Incomplete
// input yields diagnostics; nothing is faked with empty/default values.
type Diagnostic struct {
	Code     string `json:"code"`
	Channel  string `json:"channel,omitempty"`
	Sample   int64  `json:"sample,omitempty"`
	RefID    string `json:"ref_id,omitempty"`
	Position string `json:"position,omitempty"`
	Message  string `json:"message"`
}

// Evidence is a machine-readable judgment record emitted by the decoder,
// e.g. why multiple candidates were kept or why a gap was not spliced.
type Evidence struct {
	Code    string   `json:"code"`
	Channel string   `json:"channel,omitempty"`
	Sample  int64    `json:"sample,omitempty"`
	Refs    []string `json:"refs,omitempty"`
	Message string   `json:"message"`
}

// BitRef links one decoded bit back to the edges that produced it.
type BitRef struct {
	Bit       int    `json:"bit"`
	Value     int    `json:"value"`
	ClockEdge string `json:"clock_edge"` // edge ID; "" means the clock edge was missing
	DataEdge  string `json:"data_edge,omitempty"`
	Sample    int64  `json:"sample"`
}

// DecodedByte is a byte with full edge provenance.
type DecodedByte struct {
	ID         string   `json:"id"`
	Index      int      `json:"index"`
	Value      int      `json:"value"`
	Ack        *bool    `json:"ack,omitempty"`  // I2C: true=ACK, false=NACK
	Peer       *int     `json:"peer,omitempty"` // SPI: simultaneous MISO byte
	Bits       []BitRef `json:"bits"`
	Stretched  bool     `json:"stretched,omitempty"`
	Confidence float64  `json:"confidence"`
}

// Transaction is a decoded bus transaction.
type Transaction struct {
	ID          string        `json:"id"`
	Protocol    string        `json:"protocol"`
	Device      string        `json:"device"` // CS alias for SPI, "bus" otherwise
	StartSample int64         `json:"start_sample"`
	EndSample   int64         `json:"end_sample"`
	Status      string        `json:"status"` // complete | truncated_gap | aborted
	Segments    int           `json:"segments"`
	Bytes       []DecodedByte `json:"bytes"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
}

// Candidate is one legal decode of a scope. Ambiguity (missing clock edge,
// unknown SPI mode, boundary sampling) keeps several candidates; both
// adopted and unadopted ones are preserved.
type Candidate struct {
	ID           string        `json:"id"`
	Protocol     string        `json:"protocol"`
	Scope        string        `json:"scope"` // e.g. bus:i2c, device:CS_A, bus:sync
	Confidence   float64       `json:"confidence"`
	Rationale    []string      `json:"rationale"`
	Adopted      bool          `json:"adopted"`
	Transactions []Transaction `json:"transactions"`
}

// RunStatus distinguishes the three hard outcomes plus success.
type RunStatus string

const (
	StatusOK              RunStatus = "ok"
	StatusBudgetExhausted RunStatus = "budget_exhausted"
	StatusFormatCorrupt   RunStatus = "format_corrupt"
	StatusUnsolvable      RunStatus = "unsolvable"
)

// RunRecord is a derived result: input digest + parameter version + parents.
type RunRecord struct {
	ID             string       `json:"id"`
	InputDigest    string       `json:"input_digest"`
	CaptureID      string       `json:"capture_id"`
	ConfigID       string       `json:"config_id"`
	ParamsID       string       `json:"params_id"`
	ParamsVersion  int          `json:"params_version"`
	DecoderVersion string       `json:"decoder_version"`
	Parents        []string     `json:"parents"`
	Status         RunStatus    `json:"status"`
	Diagnostics    []Diagnostic `json:"diagnostics,omitempty"`
	Evidence       []Evidence   `json:"evidence,omitempty"`
	Candidates     []Candidate  `json:"candidates,omitempty"`
}

// Event is an append-only log entry. Replaying events 1..N restores the
// dependency versions and visible conclusions of that moment.
type Event struct {
	Seq     int64           `json:"seq"`
	ID      string          `json:"id"`
	Batch   string          `json:"batch"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	Parents []string        `json:"parents"`
}

// Decision is a human verdict on a candidate / byte / transaction.
type Decision struct {
	Target  string `json:"target"`
	Verdict string `json:"verdict"` // accept | reject
	Author  string `json:"author"`
	Note    string `json:"note,omitempty"`
}

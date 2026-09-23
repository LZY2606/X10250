// Package model holds the domain types for the edge decoding bench.
//
// The bench separates three layers deliberately:
//
//   - immutable material: captures, channel aliases, probe configuration,
//     original edge timing representations and decoder version;
//   - decisions: revision of decoding parameters and human accept/reject
//     actions, each carrying a parent dependency;
//   - derived results: decoded frames, bytes and bus events produced by a
//     named decoder version from a capture and a parameter revision.
//
// Nothing in this package depends on SQLite or the network so the rules can
// be invoked directly from tests.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// DecoderVersion is the rule version stamped on every derived result.
const DecoderVersion = "edge-decoder/2026-09.0"

// SchemaVersion is the case pack / project file version.
const SchemaVersion = 1

func canonicalJSON(v any) []byte {
	// MarshalJSON implementations in this package are already canonical
	// (maps are sorted before construction); json.Marshal sorts map keys.
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Digest returns a stable hex SHA-256 over the canonical JSON of v.
func Digest(v any) string {
	sum := sha256.Sum256(canonicalJSON(v))
	return hex.EncodeToString(sum[:16])
}

// Level is the normalised digital level of a channel at an edge.
type Level string

const (
	LevelLow  Level = "L"
	LevelHigh Level = "H"
)

// Edge is one transition on one digital channel.
//
// RawTime keeps the imported representation verbatim (for example
// "12.5ms" or "12500e-6"). Sample is the zero based capture sample index,
// TSeconds is the normalised time in seconds and Valid reports whether
// normalisation succeeded.
type Edge struct {
	Seq       int     `json:"seq"`
	Channel   string  `json:"channel"`
	Sample    int64   `json:"sample"`
	TSeconds  float64 `json:"t_seconds,omitempty"`
	RawTime   string  `json:"raw_time"`
	TimeUnit  string  `json:"time_unit,omitempty"`
	Level     Level   `json:"level"`
	RawLevel  string  `json:"raw_level"`
	SourceRef string  `json:"source_ref"`
	Valid     bool    `json:"valid"`
}

// Channel describes an alias and its probe wiring.
type Channel struct {
	Alias     string `json:"alias"`
	Physical  string `json:"physical"`
	Inverted  bool   `json:"inverted"`
	IdleLevel Level  `json:"idle_level"`
	RawInvert string `json:"raw_invert,omitempty"`
	RawIdle   string `json:"raw_idle,omitempty"`
}

// Capture is an immutable digital capture with its probe configuration.
type Capture struct {
	ID         string    `json:"id"`
	Revision   int       `json:"revision"`
	Supersedes string    `json:"supersedes,omitempty"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title"`
	SampleRate float64   `json:"sample_rate_hz"`
	RawRate    string    `json:"raw_sample_rate"`
	RateUnit   string    `json:"rate_unit"`
	ProbeModel string    `json:"probe_model"`
	Channels   []Channel `json:"channels"`
	Edges      []Edge    `json:"edges"`
	// RawDocument is the verbatim imported JSON for this capture.
	RawDocument string `json:"-"`
	Notes       string `json:"notes,omitempty"`
}

// ClockPolarity captures the clock polarity statement recorded at import
// time. It is part of the immutable probe evidence; parameter revision
// introduces a new Params revision instead of mutating this.
type ClockPolarity struct {
	Bus       string `json:"bus"`
	CPOL      int    `json:"cpol"`
	Raw       string `json:"raw"`
	SourceRef string `json:"source_ref"`
}

// ProbeEvidence is the immutable clock/probe statement of a capture.
type ProbeEvidence struct {
	CaptureID string          `json:"capture_id"`
	Polarity  []ClockPolarity `json:"clock_polarity"`
}

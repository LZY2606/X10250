// Package model defines the immutable material layer of the edge decoding desk.
// Every domain quantity carries its unit or coordinate basis; the raw
// representation is preserved at import and normalization happens only in
// the derived layer.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Channel describes one probe channel. Alias, inversion and clock polarity
// are immutable material: they are captured at import and never rewritten.
type Channel struct {
	Alias         string `json:"alias"`
	Role          string `json:"role"` // sda|scl|sclk|mosi|miso|cs|clock|data
	Inverted      bool   `json:"inverted"`
	ClockPolarity string `json:"clock_polarity"` // rising|falling ("" when n/a)
}

// ProbeConfig is the probe configuration imported alongside the edges.
type ProbeConfig struct {
	Channels []Channel `json:"channels"`
}

// Import is one immutable capture of digital channel edges plus probe config.
type Import struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	CreatedAt      string      `json:"created_at"`
	SampleRateRaw  string      `json:"sample_rate_raw"` // original representation, e.g. "100MHz"
	SampleRateHz   float64     `json:"sample_rate_hz"`  // normalized derived value
	SampleRateOK   bool        `json:"sample_rate_ok"`
	DecoderVersion string      `json:"decoder_version"`
	Probe          ProbeConfig `json:"probe"`
	ParentImport   string      `json:"parent_import,omitempty"` // source-patch lineage
	ChangedSpan    *Span       `json:"changed_span,omitempty"`  // edge index span changed vs parent
	Digest         string      `json:"digest"`
}

// Span is an inclusive edge-index interval in the capture coordinate basis.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Contains reports whether the spans overlap.
func (s Span) Overlaps(o Span) bool { return s.Start <= o.End && o.Start <= s.End }

// Edge is one immutable digital edge. TimeRaw keeps the original textual
// representation; TimeNS is the normalized derived value. When the raw value
// cannot be converted, TimeOK is false and the edge stays in place with a
// diagnostic instead of being dropped.
type Edge struct {
	ImportID string `json:"import_id"`
	Idx      int    `json:"idx"`
	Channel  string `json:"channel"`
	TimeRaw  string `json:"time_raw"`
	TimeNS   int64  `json:"time_ns"`
	TimeOK   bool   `json:"time_ok"`
	Level    int    `json:"level"` // 0 or 1, after probe inversion is NOT applied here
}

var timeRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+(?:[eE][+-]?[0-9]+)?)(ns|us|µs|ms|s)$`)

// ParseTime converts a raw time literal with unit into nanoseconds.
func ParseTime(raw string) (int64, bool) {
	m := timeRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	var mult float64
	switch m[2] {
	case "ns":
		mult = 1
	case "us", "µs":
		mult = 1e3
	case "ms":
		mult = 1e6
	case "s":
		mult = 1e9
	}
	return int64(v*mult + 0.5), true
}

var rateRe = regexp.MustCompile(`^([0-9]*\.?[0-9]+(?:[eE][+-]?[0-9]+)?)(Hz|kHz|MHz|GHz)$`)

// ParseSampleRate converts a raw sample-rate literal into Hz.
func ParseSampleRate(raw string) (float64, bool) {
	m := rateRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "Hz":
		return v, true
	case "kHz":
		return v * 1e3, true
	case "MHz":
		return v * 1e6, true
	case "GHz":
		return v * 1e9, true
	}
	return 0, false
}

// Digest returns a stable content digest for any JSON-marshalable value.
func Digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ID builds a stable identity from a kind prefix and content.
func ID(kind string, v any) string {
	d := Digest(v)
	return fmt.Sprintf("%s-%s", kind, d[:16])
}

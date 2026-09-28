// Package model holds the immutable evidence layer (captured edges, probe
// configuration) and versioned decode parameters. Raw textual representations
// are preserved at import; only the derived layer normalizes units.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

const TimeUnitNs = "ns"

// Edge is a single digital transition exactly as recorded by the probe.
type Edge struct {
	ID      string `json:"id"`
	Channel string `json:"channel"`
	Raw     string `json:"raw"`      // original textual timestamp representation
	TimeNs  int64  `json:"time_ns"`  // normalized; -1 when unconvertible
	Level   int    `json:"level"`    // logic level after the edge (0/1)
}

// Channel is the probe alias / electrical configuration for one line.
type Channel struct {
	ID     string `json:"id"`
	Alias  string `json:"alias"`
	Role   string `json:"role"` // sda, scl, clk, mosi, miso, cs, data, aux
	Invert bool   `json:"invert"`
	Device string `json:"device,omitempty"` // device tag for chip-select lines
}

// Segment is a declared valid capture interval (coordinate basis).
type Segment struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	StartNs int64  `json:"start_ns"`
	EndNs   int64  `json:"end_ns"`
}

// Capture is the immutable material imported from the logic analyzer.
type Capture struct {
	SampleRateHz  float64   `json:"sample_rate_hz"`
	TimeUnit      string    `json:"time_unit"`
	ClockPolarity string    `json:"clock_polarity"` // recorded by the probe, kept as evidence
	Channels      []Channel `json:"channels"`
	Edges         []Edge    `json:"edges"`
	Segments      []Segment `json:"segments"`
}

// Params are the mutable, versioned decoder settings.
type Params struct {
	Version           int                `json:"version"`
	ClockPolarity     string             `json:"clock_polarity"` // rising | falling
	SPIMode           string             `json:"spi_mode"`       // 0..3 | unknown
	WordSize          int                `json:"word_size"`
	BoundaryEpsilonNs int64              `json:"boundary_epsilon_ns"`
	GapThresholdNs    int64              `json:"gap_threshold_ns"`
	StretchFactor     float64            `json:"stretch_factor"`
	DecoderVersions   map[string]string  `json:"decoder_versions"`
}

// Event is one append-only, hash-chained entry in the audit log.
type Event struct {
	Seq       int64           `json:"seq"`
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	View      json.RawMessage `json:"view"`
	Digest    string          `json:"digest"`
	CreatedAt string          `json:"created_at"`
}

// NormalizeError is returned when an edge timestamp cannot be converted. The
// edge stays in place (TimeNs == -1) instead of being silently dropped.
type NormalizeError struct {
	EdgeID  string `json:"edge_id"`
	Channel string `json:"channel"`
	Raw     string `json:"raw"`
	Reason  string `json:"reason"`
}

// Digest is a stable content digest used for identity and dependency chains.
func Digest(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// ParseTime converts the preserved original representation to nanoseconds.
// It supports ns/us/µs/ms/s suffixes and plain integer nanoseconds.
func ParseTime(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	var mult int64 = 1
	switch {
	case strings.HasSuffix(s, "ns"):
		s, mult = strings.TrimSpace(s[:len(s)-2]), 1
	case strings.HasSuffix(s, "us"), strings.HasSuffix(s, "µs"):
		n := 2
		if strings.HasSuffix(s, "µs") {
			n = len("µs")
		}
		s, mult = strings.TrimSpace(s[:len(s)-n]), 1000
	case strings.HasSuffix(s, "ms"):
		s, mult = strings.TrimSpace(s[:len(s)-2]), 1_000_000
	case strings.HasSuffix(s, "s"):
		s, mult = strings.TrimSpace(s[:len(s)-1]), 1_000_000_000
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(f * float64(mult)), true
}

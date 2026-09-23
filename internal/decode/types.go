// Package decode translates raw logic-analyzer edges into reviewable
// I2C, SPI and custom synchronous transactions.
//
// Layering rule: the material layer keeps the original representation
// (raw time strings, units, uninverted levels). Normalization happens
// only in the derived layer; values that cannot be converted stay in
// place and carry a diagnostic instead of being silently dropped.
package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Status is the terminal state of a decode run. Budget exhaustion,
// corrupt format and semantic no-solution are three distinct results.
type Status string

const (
	StatusOK              Status = "ok"
	StatusBudgetExhausted Status = "budget_exhausted"
	StatusCorruptFormat   Status = "corrupt_format"
	StatusUnsolvable      Status = "unsolvable"
)

// Machine-readable event codes emitted while decoding.
const (
	EvCandidateRetained = "candidate.retained"
	EvGapSplit          = "transaction.gap_split"
	EvDecodeStatus      = "decode.status"

	ReasonMissingClockEdge = "missing_clock_edge"
	ReasonRepeatedStart    = "repeated_start"
	ReasonClockStretch     = "clock_stretch"
	ReasonSPIModeUnknown   = "spi_mode_unknown"
	ReasonBoundarySample   = "boundary_sample"
)

// ChannelConfig is part of the immutable probe configuration.
type ChannelConfig struct {
	Alias         string `json:"alias"`
	Bus           string `json:"bus"`  // "i2c", "spi", "sync"
	Role          string `json:"role"` // "scl","sda","clock","data","mosi","miso","cs"
	Inverted      bool   `json:"inverted"`
	ClockPolarity string `json:"clock_polarity"` // "rising" | "falling" (captured preference)
}

// Edge is one captured digital transition. TimeRaw/Unit preserve the
// original representation exactly as imported.
type Edge struct {
	Channel string `json:"channel"`
	TimeRaw string `json:"time_raw"`
	Unit    string `json:"unit"` // "ps","ns","us","ms","samples"
	Level   int    `json:"level"`
}

// Material is the immutable imported evidence: original edge times,
// sample rate, channel aliases, inversion, clock polarity and the
// decoder version are all preserved.
type Material struct {
	ID             string          `json:"id"`
	Version        int             `json:"version"`
	Source         string          `json:"source"`
	SampleRateHz   float64         `json:"sample_rate_hz"`
	DecoderVersion string          `json:"decoder_version"`
	Channels       []ChannelConfig `json:"channels"`
	Edges          []Edge          `json:"edges"`
}

// Params is a versioned, modifiable decision layer on top of material.
type Params struct {
	ID                 string            `json:"id"`
	Version            int               `json:"version"`
	ClockPolarity      map[string]string `json:"clock_polarity"` // bus -> "rising"|"falling"
	SPIMode            string            `json:"spi_mode"`       // "unknown" | "0".."3"
	GapThresholdPS     int64             `json:"gap_threshold_ps"`
	StretchThresholdPS int64             `json:"stretch_threshold_ps"`
	BoundaryWindowPS   int64             `json:"boundary_window_ps"`
	MaxCandidates      int               `json:"max_candidates"`
	MaxEdges           int               `json:"max_edges"`
}

// NormEdge is a derived-layer edge: normalized time base (picoseconds)
// and inversion applied. Index points back to the original material edge.
type NormEdge struct {
	Index   int    `json:"index"`
	Channel string `json:"channel"`
	TimePS  int64  `json:"time_ps"`
	Level   int    `json:"level"`
}

// Diagnostic is attached to a position; we never fake success with
// empty or default values when input is incomplete.
type Diagnostic struct {
	Severity  string `json:"severity"` // "error" | "warning"
	Code      string `json:"code"`
	Message   string `json:"message"`
	Channel   string `json:"channel,omitempty"`
	EdgeIndex *int   `json:"edge_index,omitempty"`
	TimePS    *int64 `json:"time_ps,omitempty"`
}

// Event is a machine-readable judgment record.
type Event struct {
	Kind   string         `json:"kind"`
	Code   string         `json:"code,omitempty"`
	Detail map[string]any `json:"detail,omitempty"`
}

// Byte is one decoded byte; EdgeStart/EdgeEnd index into the material
// edge list so any byte can be traced back to its edges.
type Byte struct {
	ID          string  `json:"id"`
	Value       int     `json:"value"`
	Ack         *bool   `json:"ack,omitempty"` // I2C only: true=ACK, false=NACK
	EdgeStart   int     `json:"edge_start"`
	EdgeEnd     int     `json:"edge_end"`
	TimeStartPS int64   `json:"time_start_ps"`
	TimeEndPS   int64   `json:"time_end_ps"`
	CS          string  `json:"cs,omitempty"`
	Confidence  float64 `json:"confidence"`
}

// Transaction is a decoded transfer. GapAfter marks a transaction that
// ended because of a data gap; bytes across a gap are never stitched.
type Transaction struct {
	ID       string `json:"id"`
	Decoder  string `json:"decoder"` // "i2c" | "spi" | "sync"
	Device   string `json:"device,omitempty"`
	Bytes    []Byte `json:"bytes"`
	StartPS  int64  `json:"start_ps"`
	EndPS    int64  `json:"end_ps"`
	GapAfter bool   `json:"gap_after,omitempty"`
}

// Candidate is one decode hypothesis. Ambiguous situations (missing
// clock edge, repeated START, clock stretching, unknown SPI mode,
// boundary sampling) keep every legal candidate.
type Candidate struct {
	ID           string            `json:"id"`
	Bus          string            `json:"bus"`
	Confidence   float64           `json:"confidence"`
	Reasons      []string          `json:"reasons"`
	Params       map[string]string `json:"params"`
	Transactions []Transaction     `json:"transactions"`
}

// Result is the full derived output of one decode run.
type Result struct {
	Status         Status       `json:"status"`
	InputDigest    string       `json:"input_digest"`
	DecoderVersion string       `json:"decoder_version"`
	Candidates     []Candidate  `json:"candidates"`
	Diagnostics    []Diagnostic `json:"diagnostics"`
	Events         []Event      `json:"events"`
}

// Digest produces a stable identity for equivalent inputs.
func Digest(parts ...any) string {
	h := sha256.New()
	for _, p := range parts {
		b, _ := json.Marshal(p)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func shortID(prefix, seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return prefix + "-" + hex.EncodeToString(sum[:])[:12]
}

// channelConfigs indexes channel config by alias.
func (m *Material) channelConfigs() map[string]ChannelConfig {
	out := make(map[string]ChannelConfig, len(m.Channels))
	for _, c := range m.Channels {
		out[c.Alias] = c
	}
	return out
}

var unitToPS = map[string]float64{
	"ps": 1, "ns": 1e3, "us": 1e6, "ms": 1e9, "s": 1e12,
}

// Normalize converts the raw material into the derived time base.
// Unconvertible values stay in the material untouched and produce a
// positioned diagnostic here; structurally broken input (negative or
// non-monotonic times) is reported as corrupt.
func Normalize(m *Material) (edges []NormEdge, diags []Diagnostic, corrupt bool) {
	cfgs := m.channelConfigs()
	lastByChan := map[string]int64{}
	for i, e := range m.Edges {
		idx := i
		cfg, known := cfgs[e.Channel]
		if !known {
			diags = append(diags, Diagnostic{Severity: "error", Code: "channel_unknown",
				Message: fmt.Sprintf("edge %d references unknown channel %q", i, e.Channel),
				Channel: e.Channel, EdgeIndex: &idx})
			corrupt = true
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(e.TimeRaw), 64)
		if err != nil {
			diags = append(diags, Diagnostic{Severity: "warning", Code: "time_unparseable",
				Message: fmt.Sprintf("edge %d time %q cannot be parsed; left in place", i, e.TimeRaw),
				Channel: e.Channel, EdgeIndex: &idx})
			continue
		}
		if v < 0 {
			diags = append(diags, Diagnostic{Severity: "error", Code: "time_negative",
				Message: fmt.Sprintf("edge %d has negative time %q", i, e.TimeRaw),
				Channel: e.Channel, EdgeIndex: &idx})
			corrupt = true
			continue
		}
		var tps int64
		if e.Unit == "samples" {
			if m.SampleRateHz <= 0 {
				diags = append(diags, Diagnostic{Severity: "warning", Code: "unit_unconvertible",
					Message: fmt.Sprintf("edge %d uses samples but sample rate is unknown; left in place", i),
					Channel: e.Channel, EdgeIndex: &idx})
				continue
			}
			tps = int64(v / m.SampleRateHz * 1e12)
		} else if mult, ok := unitToPS[e.Unit]; ok {
			tps = int64(v * mult)
		} else {
			diags = append(diags, Diagnostic{Severity: "warning", Code: "unit_unknown",
				Message: fmt.Sprintf("edge %d unit %q unknown; left in place", i, e.Unit),
				Channel: e.Channel, EdgeIndex: &idx})
			continue
		}
		if last, seen := lastByChan[e.Channel]; seen && tps < last {
			diags = append(diags, Diagnostic{Severity: "error", Code: "time_nonmonotonic",
				Message: fmt.Sprintf("edge %d on %s goes backwards in time", i, e.Channel),
				Channel: e.Channel, EdgeIndex: &idx})
			corrupt = true
			continue
		}
		lastByChan[e.Channel] = tps
		lvl := e.Level
		if cfg.Inverted {
			lvl = 1 - lvl
		}
		edges = append(edges, NormEdge{Index: i, Channel: e.Channel, TimePS: tps, Level: lvl})
	}
	// Stable ordering: equivalent inputs in any insertion order decode
	// to the same result.
	sort.SliceStable(edges, func(a, b int) bool {
		if edges[a].TimePS != edges[b].TimePS {
			return edges[a].TimePS < edges[b].TimePS
		}
		return edges[a].Index < edges[b].Index
	})
	return edges, diags, corrupt
}

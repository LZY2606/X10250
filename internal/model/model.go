// Package model defines the domain types for the edge-decode workbench.
// Immutable material (imports: raw edges, sample rate, aliases, inversion,
// clock polarity, decoder version) is kept separate from derived results
// (decodes, candidates, decisions), which always record their input digest,
// parameter version and parent dependencies.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Decode result statuses. Budget exhaustion, format corruption and semantic
// dead-ends are three distinct outcomes, never collapsed into one.
const (
	StatusOK                = "ok"
	StatusAmbiguous         = "ambiguous"
	StatusResourceExhausted = "resource_exhausted"
	StatusCorrupt           = "corrupt_format"
	StatusUnsolvable        = "unsolvable"
)

// Judgment rule identifiers (machine readable).
const (
	RuleMissingClock   = "keep-candidates-on-missing-clock"
	RuleRepeatedStart  = "repeated-start-keeps-transaction"
	RuleClockStretch   = "clock-stretch-tolerated"
	RuleUnknownSPIMode = "keep-candidates-on-unknown-spi-mode"
	RuleNoStitchOnGap  = "no-stitch-on-data-gap"
)

// HashID derives a stable, content-addressed identity.
func HashID(prefix string, parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(sum[:])[:16])
}

// Diagnostic is a positioned problem report; incomplete input is reported
// this way instead of being silently defaulted.
type Diagnostic struct {
	Severity string `json:"severity"` // error | warning
	Path     string `json:"path"`     // e.g. edges[17].time, config.channels
	Message  string `json:"message"`
}

// Edge is one recorded digital transition. Raw and Unit preserve the
// original representation; TimeNS is filled by the derived layer.
type Edge struct {
	Channel string `json:"channel"`
	Raw     string `json:"raw"`
	Unit    string `json:"unit"` // ns | us | ms | s | samples
	TimeNS  int64  `json:"time_ns"`
	Level   int    `json:"level"` // 0/1 as recorded, before inversion
}

// ChannelConfig describes one probe channel.
type ChannelConfig struct {
	Name     string `json:"name"`
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Role     string `json:"role"` // scl|sda|sclk|mosi|miso|cs|clock|data
}

// ProbeConfig is immutable import material.
type ProbeConfig struct {
	SampleRateHz   int64           `json:"sample_rate_hz"`
	ClockPolarity  string          `json:"clock_polarity"` // rising | falling
	DecoderVersion string          `json:"decoder_version"`
	Channels       []ChannelConfig `json:"channels"`
}

// Import is an immutable bundle of edges + probe configuration.
type Import struct {
	ID       string      `json:"id"`
	ParentID string      `json:"parent_id,omitempty"`
	Change   string      `json:"change,omitempty"`
	Label    string      `json:"label"`
	Config   ProbeConfig `json:"config"`
	Edges    []Edge      `json:"edges"`
	Digest   string      `json:"digest"`
	Seq      int64       `json:"seq,omitempty"`
}

// ComputeID assigns the content-addressed identity and digest.
func (imp *Import) ComputeID() {
	imp.Digest = HashID("dig", imp.Config, imp.Edges)
	imp.ID = HashID("imp", imp.Config, imp.Edges, imp.ParentID, imp.Change)
}

// Channel looks up a channel config by name.
func (imp *Import) Channel(name string) *ChannelConfig {
	for i := range imp.Config.Channels {
		if imp.Config.Channels[i].Name == name {
			return &imp.Config.Channels[i]
		}
	}
	return nil
}

// LEdge is a normalized edge: nanoseconds, inversion applied, import index kept.
type LEdge struct {
	T   int64 `json:"t"`
	Lvl int   `json:"lvl"`
	Idx int   `json:"idx"` // index into Import.Edges
}

// ToNS converts a raw time value with unit into nanoseconds.
func ToNS(raw, unit string, rateHz int64) (int64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable time value %q", raw)
	}
	switch unit {
	case "ns":
		return int64(math.Round(f)), nil
	case "us":
		return int64(math.Round(f * 1e3)), nil
	case "ms":
		return int64(math.Round(f * 1e6)), nil
	case "s":
		return int64(math.Round(f * 1e9)), nil
	case "samples":
		if rateHz <= 0 {
			return 0, fmt.Errorf("unit 'samples' requires positive sample_rate_hz")
		}
		return int64(math.Round(f * 1e9 / float64(rateHz))), nil
	default:
		return 0, fmt.Errorf("unknown time unit %q", unit)
	}
}

// Normalized converts raw edges into the derived representation: units to
// nanoseconds, inversion applied, stable ordering by (time, original index).
// Unconvertible values stay in place in the import and are reported with
// their position; they are never replaced by defaults.
func (imp *Import) Normalized() (map[string][]LEdge, []Diagnostic) {
	out := map[string][]LEdge{}
	var diags []Diagnostic
	inv := map[string]bool{}
	known := map[string]bool{}
	for _, c := range imp.Config.Channels {
		inv[c.Name] = c.Inverted
		known[c.Name] = true
	}
	for i, e := range imp.Edges {
		path := fmt.Sprintf("edges[%d]", i)
		if !known[e.Channel] {
			diags = append(diags, Diagnostic{"error", path + ".channel",
				fmt.Sprintf("channel %q not present in probe config", e.Channel)})
			continue
		}
		ns, err := ToNS(e.Raw, e.Unit, imp.Config.SampleRateHz)
		if err != nil {
			diags = append(diags, Diagnostic{"error", path + ".time", err.Error()})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, Diagnostic{"error", path + ".level",
				fmt.Sprintf("level must be 0 or 1, got %d", e.Level)})
			continue
		}
		lvl := e.Level
		if inv[e.Channel] {
			lvl = 1 - lvl
		}
		out[e.Channel] = append(out[e.Channel], LEdge{T: ns, Lvl: lvl, Idx: i})
	}
	for ch := range out {
		es := out[ch]
		sort.SliceStable(es, func(a, b int) bool { return es[a].T < es[b].T })
		out[ch] = es
	}
	return out, diags
}

// Params is a versioned, derived-layer parameter set.
type Params struct {
	ID              string `json:"id"`
	Version         int    `json:"version"`
	ClockPolarity   string `json:"clock_polarity"`
	SPIMode         string `json:"spi_mode"` // 0|1|2|3|unknown
	SampleOffsetNS  int64  `json:"sample_offset_ns"`  // boundary guard band
	StretchThreshNS int64  `json:"stretch_threshold_ns"`
	GapThreshNS     int64  `json:"gap_threshold_ns"`
	MaxEdges        int    `json:"max_edges"`
	MaxCandidates   int    `json:"max_candidates"`
	DecoderVersion  string `json:"decoder_version"`
	CSActiveLow     bool   `json:"cs_active_low"`
	WordSize        int    `json:"word_size"`
}

// DefaultParams returns parameter version 1.
func DefaultParams() *Params {
	p := &Params{
		Version:         1,
		ClockPolarity:   "rising",
		SPIMode:         "unknown",
		SampleOffsetNS:  40,
		StretchThreshNS: 2000,
		GapThreshNS:     20000,
		MaxEdges:        100000,
		MaxCandidates:   64,
		DecoderVersion:  "dec/1.0.0",
		CSActiveLow:     true,
		WordSize:        8,
	}
	p.ComputeID()
	return p
}

// ComputeID derives the parameter identity from its content.
func (p *Params) ComputeID() {
	p.ID = HashID("par", p.Version, p.ClockPolarity, p.SPIMode, p.SampleOffsetNS,
		p.StretchThreshNS, p.GapThreshNS, p.MaxEdges, p.MaxCandidates,
		p.DecoderVersion, p.CSActiveLow, p.WordSize)
}

// Annotation marks a structural observation inside a transaction.
type Annotation struct {
	Type   string `json:"type"` // repeated_start|clock_stretch|gap|partial_byte
	AtNS   int64  `json:"at_ns"`
	EndNS  int64  `json:"end_ns,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Byte is one decoded byte/word with full edge provenance.
type Byte struct {
	ID           string `json:"id"`
	Index        int    `json:"index"`
	Value        int    `json:"value"`
	Role         string `json:"role"` // addr | data | word
	Ack          *bool  `json:"ack,omitempty"` // true=ACK, false=NACK (i2c only)
	BoundaryRisk bool   `json:"boundary_risk,omitempty"`
	EdgeRefs     []int  `json:"edge_refs"` // import edge indexes
	StartNS      int64  `json:"start_ns"`
	EndNS        int64  `json:"end_ns"`
}

// Transaction is one bus transaction; gaps are never stitched together.
type Transaction struct {
	ID          string       `json:"id"`
	Kind        string       `json:"kind"` // i2c | spi | sync
	Lane        string       `json:"lane"` // channel carrying the data
	Device      string       `json:"device,omitempty"`
	Status      string       `json:"status"` // complete | gap_truncated | truncated
	StartNS     int64        `json:"start_ns"`
	EndNS       int64        `json:"end_ns"`
	Bytes       []Byte       `json:"bytes"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Candidate is one competing decode hypothesis.
type Candidate struct {
	ID           string        `json:"id"`
	Protocol     string        `json:"protocol"`
	Hypothesis   string        `json:"hypothesis"`
	Confidence   float64       `json:"confidence"`
	Transactions []Transaction `json:"transactions"`
}

// Judgment is a machine-readable decision basis emitted by the decoder.
type Judgment struct {
	Rule    string `json:"rule"`
	Subject string `json:"subject"`
	Basis   string `json:"basis"`
	Adopted bool   `json:"adopted"`
}

// DecodeResult records input digest, parameter version and parent deps.
type DecodeResult struct {
	ID          string       `json:"id"`
	ImportID    string       `json:"import_id"`
	ParamsID    string       `json:"params_id"`
	ParamsVer   int          `json:"params_version"`
	InputDigest string       `json:"input_digest"`
	Status      string       `json:"status"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Candidates  []Candidate  `json:"candidates"`
	Judgments   []Judgment   `json:"judgments,omitempty"`
	Seq         int64        `json:"seq,omitempty"`
}

// Event is one entry in the append-only evidence log.
type Event struct {
	Seq      int64           `json:"seq"`
	ID       string          `json:"id"`
	Kind     string          `json:"kind"` // import|mutate|params|decode|judgment|decision|export|import_archive
	Payload  json.RawMessage `json:"payload"`
	Parents  []string        `json:"parents,omitempty"`
	Checksum string          `json:"checksum"`
}

// Decision is a human accept/reject on a candidate.
type Decision struct {
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // accept | reject
	Reason      string `json:"reason"`
	Reviewer    string `json:"reviewer"`
	Seq         int64  `json:"seq,omitempty"`
}

// Package model defines the domain types of the edge decoding workbench.
// Materials are immutable; derivations and decisions are append-only events.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---------- immutable materials ----------

// Channel describes one digital probe channel. Raw configuration is kept
// verbatim; the derived layer applies inversion and unit normalization.
type Channel struct {
	Alias    string `json:"alias"`
	Index    int    `json:"index"`
	Inverted bool   `json:"inverted"`
	Initial  int    `json:"initial"` // level before the first edge (0/1)
}

// Edge is one digital transition. TRaw preserves the original textual
// representation; TNs is the normalized value in nanoseconds (0 when the
// raw value could not be converted, see diagnostics).
type Edge struct {
	ID      string `json:"id"`
	Ch      string `json:"ch"` // channel alias
	TRaw    string `json:"t_raw"`
	TNs     int64  `json:"t_ns"`
	Level   int    `json:"level"`             // raw level as captured (before inversion)
	Invalid bool   `json:"invalid,omitempty"` // unconvertible raw value, kept in place
}

// EdgeStream is an immutable capture material.
type EdgeStream struct {
	ID           string    `json:"id"`
	SampleRateHz int64     `json:"sample_rate_hz"`
	TimeUnit     string    `json:"time_unit"` // always "ns" for normalized values
	Channels     []Channel `json:"channels"`
	Edges        []Edge    `json:"edges"`
	Source       string    `json:"source"`
	Revision     int       `json:"revision"`
	SupersedesID string    `json:"supersedes_id,omitempty"`
}

// ProbeConfig is an immutable probe/decoder configuration material.
type ProbeConfig struct {
	ID             string            `json:"id"`
	DecoderVersion string            `json:"decoder_version"`
	ClockPolarity  string            `json:"clock_polarity"`  // "rising" | "falling"
	SPIMode        string            `json:"spi_mode"`        // "0".."3" | "unknown"
	SampleBoundary string            `json:"sample_boundary"` // "leading" | "trailing"
	CSActiveLevel  int               `json:"cs_active_level"`
	Roles          map[string]string `json:"roles"` // role -> channel alias (scl,sda,sck,mosi,miso,cs0,cs1,clk,data)
	Revision       int               `json:"revision"`
	SupersedesID   string            `json:"supersedes_id,omitempty"`
}

// Params are the effective decode parameters of one derivation run.
type Params struct {
	DecoderVersion           string `json:"decoder_version"`
	ClockPolarity            string `json:"clock_polarity"`
	SPIMode                  string `json:"spi_mode"`
	SampleBoundary           string `json:"sample_boundary"`
	CSActiveLevel            int    `json:"cs_active_level"`
	GapThresholdNs           int64  `json:"gap_threshold_ns"`
	StretchThresholdNs       int64  `json:"stretch_threshold_ns"`
	BoundaryWindowNs         int64  `json:"boundary_window_ns"`
	MaxOps                   int    `json:"max_ops"`
	TreatRepeatedStartAsStop bool   `json:"treat_repeated_start_as_stop"`
	AllowGapStitching        bool   `json:"allow_gap_stitching"` // must stay false per policy; kept for counterexamples
}

// DefaultParams derives effective params from a probe config.
func DefaultParams(pc ProbeConfig) Params {
	return Params{
		DecoderVersion:     pc.DecoderVersion,
		ClockPolarity:      pc.ClockPolarity,
		SPIMode:            pc.SPIMode,
		SampleBoundary:     pc.SampleBoundary,
		CSActiveLevel:      pc.CSActiveLevel,
		GapThresholdNs:     1500,
		StretchThresholdNs: 800,
		BoundaryWindowNs:   5,
		MaxOps:             200000,
	}
}

// ---------- diagnostics ----------

// Diagnostic is a positioned, machine-readable problem report.
type Diagnostic struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "error" | "warning" | "info"
	Position string `json:"position"` // e.g. "edges[12]" or "roles.scl"
	Message  string `json:"message"`
}

// ---------- derivation results ----------

// Run status values. Budget exhaustion, corrupt format and semantic
// unsolvability are three distinct outcomes.
const (
	StatusOK              = "ok"
	StatusBudgetExhausted = "budget_exhausted"
	StatusCorruptInput    = "corrupt_input"
	StatusUnsolvable      = "unsolvable"
)

// BitSample ties one decoded bit back to its source edges.
type BitSample struct {
	Value     int    `json:"value"`
	ClockEdge string `json:"clock_edge"` // edge ID
	TNs       int64  `json:"t_ns"`
	Assumed   bool   `json:"assumed,omitempty"`
}

// Byte is one decoded byte with full edge provenance.
type Byte struct {
	ID      string      `json:"id"`
	Index   int         `json:"index"`
	Value   int         `json:"value"`
	Ack     *bool       `json:"ack,omitempty"`  // I2C only: true=ACK false=NACK
	Role    string      `json:"role,omitempty"` // "address"|"data"
	StartNs int64       `json:"start_ns"`
	EndNs   int64       `json:"end_ns"`
	Bits    []BitSample `json:"bits"`
}

// Annotation marks structured bus events on a transaction.
type Annotation struct {
	Code    string `json:"code"` // repeated_start|clock_stretch|data_gap|missing_clock_edge|partial_byte
	StartNs int64  `json:"start_ns"`
	EndNs   int64  `json:"end_ns"`
	Detail  string `json:"detail"`
}

// Transaction is one decoded bus transaction.
type Transaction struct {
	ID          string       `json:"id"`
	Bus         string       `json:"bus"` // "i2c0" | "spi0" | "sync0"
	CS          string       `json:"cs,omitempty"`
	StartNs     int64        `json:"start_ns"`
	EndNs       int64        `json:"end_ns"`
	Bytes       []Byte       `json:"bytes"`
	Annotations []Annotation `json:"annotations,omitempty"`
}

// Candidate is one legal decode hypothesis.
type Candidate struct {
	ID           string        `json:"id"`
	Decoder      string        `json:"decoder"` // "i2c" | "spi" | "sync"
	Rank         int           `json:"rank"`
	Confidence   float64       `json:"confidence"`
	Assumptions  []string      `json:"assumptions,omitempty"`
	Transactions []Transaction `json:"transactions"`
}

// RunResult is the output of one derivation run.
type RunResult struct {
	Status      string       `json:"status"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Candidates  []Candidate  `json:"candidates"`
}

// Run is a derivation: input digests + param version + parent deps + result.
type Run struct {
	ID      string    `json:"id"`
	Seq     int64     `json:"seq"`
	Params  Params    `json:"params"`
	Parents []string  `json:"parents"` // material IDs
	Digest  string    `json:"digest"`  // digest of canonical inputs
	Note    string    `json:"note,omitempty"`
	Result  RunResult `json:"result"`
	Invalid string    `json:"invalid_reason,omitempty"` // set by later events, view-time computed
}

// ---------- decisions ----------

const (
	DecisionAccept = "accept"
	DecisionReject = "reject"
	DecisionAdjust = "adjust"
)

// Decision is a human review decision. Accepted and rejected outcomes are
// both preserved forever.
type Decision struct {
	ID       string `json:"id"`
	Seq      int64  `json:"seq"`
	TargetID string `json:"target_id"` // candidate / transaction / byte ID
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Code     string `json:"code,omitempty"` // machine-readable judgment code
}

// ---------- events ----------

const (
	EvImportEdges   = "import_edges"
	EvImportConfig  = "import_config"
	EvSourcePatch   = "source_patch"
	EvParamRevision = "param_revision"
	EvDecodeRun     = "decode_run"
	EvDecision      = "decision"
)

// Event is one append-only entry in the evidence log.
type Event struct {
	Seq     int64           `json:"seq"`
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

// ---------- helpers ----------

// HashID returns a stable content-addressed ID.
func HashID(prefix string, v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return prefix + "_" + hex.EncodeToString(sum[:])[:16]
}

// Digest canonicalizes v into a short hex digest.
func Digest(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:24]
}

// ParseTimeNs converts a raw textual time to nanoseconds. Supported units:
// ns, us, ms, s and bare integers (ns). Returns ok=false when unconvertible;
// callers must keep the raw value in place and emit a diagnostic.
func ParseTimeNs(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	mult := int64(1)
	for _, u := range []struct {
		suffix string
		m      int64
	}{{"ns", 1}, {"us", 1000}, {"ms", 1000000}, {"s", 1000000000}} {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.m
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f*float64(mult) + 0.5), true
	}
	return 0, false
}

// CanonicalizeEdges sorts edges deterministically so equivalent inputs
// produce stable order and stable IDs.
func CanonicalizeEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.TNs != b.TNs {
			return a.TNs < b.TNs
		}
		if a.Ch != b.Ch {
			return a.Ch < b.Ch
		}
		return a.Level < b.Level
	})
	for i := range edges {
		edges[i].ID = fmt.Sprintf("e%05d", i)
	}
}

// FormatNs renders a normalized time with its unit for display.
func FormatNs(ns int64) string {
	if ns%1000000 == 0 && ns != 0 {
		return fmt.Sprintf("%dms", ns/1000000)
	}
	if ns%1000 == 0 && ns != 0 {
		return fmt.Sprintf("%dus", ns/1000)
	}
	return fmt.Sprintf("%dns", ns)
}

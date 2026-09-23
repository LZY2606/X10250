// Package model defines the domain types of the edge decoding workbench.
//
// Layering rule: captures (materials) are immutable once stored; every
// derived result records its input digest, parameter version and parent
// dependencies; human decisions are events and never overwrite history.
package model

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// FailureKind distinguishes the three terminal outcomes of a decode or
// import attempt. They must never be conflated.
type FailureKind string

const (
	FailureBudget   FailureKind = "budget_exhausted"    // resource budget ran out
	FailureFormat   FailureKind = "format_corrupt"      // input bytes/structure broken
	FailureSemantic FailureKind = "semantic_unsolvable" // parsed fine, no legal decode
)

// Failure is a terminal, structured result (not a log line).
type Failure struct {
	Kind        FailureKind  `json:"kind"`
	Message     string       `json:"message"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %s", f.Kind, f.Message) }

// Diagnostic always carries a position so callers never fake success with
// empty/default values.
type Diagnostic struct {
	Code      string `json:"code"`
	Severity  string `json:"severity"` // "error" | "warning" | "info"
	Message   string `json:"message"`
	Channel   string `json:"channel,omitempty"`
	EdgeIndex int    `json:"edge_index"` // -1 when not edge-bound
	TimeNs    int64  `json:"time_ns"`    // -1 when not time-bound
}

// ---------- immutable material layer ----------

// Channel is a probe configuration entry. Raw naming, alias, inversion and
// role are preserved exactly as imported.
type Channel struct {
	Name     string `json:"name"`     // physical probe name, e.g. "D0"
	Alias    string `json:"alias"`    // reviewer-facing alias, e.g. "SDA"
	Inverted bool   `json:"inverted"` // electrical inversion applied in derived layer only
	Role     string `json:"role"`     // "data" | "clock" | "cs" | "other"
}

// Edge is one raw digital transition. TimeNs is the original edge time in
// nanoseconds against the capture time base; Level is the raw (pre-inversion)
// level after the edge.
type Edge struct {
	Channel string `json:"channel"`
	TimeNs  int64  `json:"time_ns"`
	Level   int    `json:"level"`
}

// Capture is an immutable material: original edge times, sample rate,
// channel aliases, inversion flags, clock polarity evidence and decoder
// version are all preserved verbatim.
type Capture struct {
	ID             string    `json:"id"`
	ParentID       string    `json:"parent_id,omitempty"` // source-patch lineage
	Label          string    `json:"label"`
	SampleRateHz   int64     `json:"sample_rate_hz"`
	ClockPolarity  string    `json:"clock_polarity"` // "rising" | "falling" evidence from probe config
	DecoderVersion string    `json:"decoder_version"`
	Channels       []Channel `json:"channels"`
	Edges          []Edge    `json:"edges"`
}

// Digest is the stable identity of the material.
func (c *Capture) Digest() string {
	b, _ := json.Marshal(struct {
		SR int64     `json:"sr"`
		CP string    `json:"cp"`
		DV string    `json:"dv"`
		Ch []Channel `json:"ch"`
		E  []Edge    `json:"e"`
	}{c.SampleRateHz, c.ClockPolarity, c.DecoderVersion, c.Channels, c.Edges})
	return fmt.Sprintf("cap-%x", sha256.Sum256(b))[:23]
}

// ChannelByAlias finds a channel by alias (falling back to name).
func (c *Capture) ChannelByAlias(alias string) *Channel {
	for i := range c.Channels {
		if c.Channels[i].Alias == alias || c.Channels[i].Name == alias {
			return &c.Channels[i]
		}
	}
	return nil
}

// EffectiveLevel applies the inversion flag in the derived layer; the raw
// stored level is never mutated.
func (c *Capture) EffectiveLevel(ch string, raw int) int {
	for _, cc := range c.Channels {
		if cc.Name == ch || cc.Alias == ch {
			if cc.Inverted {
				return 1 - raw
			}
			return raw
		}
	}
	return raw
}

// EdgesOf returns edge indices for a channel, time-ordered (stable).
func (c *Capture) EdgesOf(alias string) []int {
	name := alias
	if ch := c.ChannelByAlias(alias); ch != nil {
		name = ch.Name
	}
	var idx []int
	for i, e := range c.Edges {
		if e.Channel == name || e.Channel == alias {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool {
		if c.Edges[idx[a]].TimeNs != c.Edges[idx[b]].TimeNs {
			return c.Edges[idx[a]].TimeNs < c.Edges[idx[b]].TimeNs
		}
		return idx[a] < idx[b]
	})
	return idx
}

// ---------- parameter layer ----------

// Params is a versioned, mutable-by-revision parameter set. A revision
// creates a new version; old versions stay addressable.
type Params struct {
	Version             int    `json:"version"`
	ClockPolarity       string `json:"clock_polarity"`       // "rising" | "falling"
	SPIMode             string `json:"spi_mode"`             // "auto" | "0".."3"
	GapThresholdNs      int64  `json:"gap_threshold_ns"`     // data-gap: never stitch across
	BoundaryWindowNs    int64  `json:"boundary_window_ns"`   // boundary-sampling ambiguity window
	StretchThresholdNs  int64  `json:"stretch_threshold_ns"` // clock-stretching detection
	BudgetMaxEdges      int    `json:"budget_max_edges"`
	BudgetMaxCandidates int    `json:"budget_max_candidates"`
}

func DefaultParams() Params {
	return Params{
		Version:             1,
		ClockPolarity:       "rising",
		SPIMode:             "auto",
		GapThresholdNs:      50_000,
		BoundaryWindowNs:    60,
		StretchThresholdNs:  8_000,
		BudgetMaxEdges:      200_000,
		BudgetMaxCandidates: 64,
	}
}

func (p Params) Digest() string {
	b, _ := json.Marshal(p)
	return fmt.Sprintf("prm-%x", sha256.Sum256(b))[:23]
}

// ---------- derived result layer ----------

// Byte is one decoded byte with full edge provenance. Every displayed value
// has a stable identity and can be traced back to edges.
type Byte struct {
	ID        string `json:"id"`
	Value     int    `json:"value"`
	Bits      int    `json:"bits"`
	Ack       string `json:"ack"` // "ack" | "nack" | "none"
	IsAddress bool   `json:"is_address,omitempty"`
	Address   int    `json:"address,omitempty"`
	ReadWrite string `json:"rw,omitempty"` // "read" | "write"
	StartNs   int64  `json:"start_ns"`
	EndNs     int64  `json:"end_ns"`
	EdgeRefs  []int  `json:"edge_refs"` // indices into Capture.Edges
}

// Transaction is one bus transaction. A data gap terminates a transaction;
// traffic after a gap is never stitched into the previous one.
type Transaction struct {
	ID       string   `json:"id"`
	Protocol string   `json:"protocol"` // "i2c" | "spi" | "sync"
	Device   string   `json:"device"`   // CS alias or I2C address
	StartNs  int64    `json:"start_ns"`
	EndNs    int64    `json:"end_ns"`
	Flags    []string `json:"flags,omitempty"` // repeated_start | clock_stretch | gap_terminated
	Bytes    []Byte   `json:"bytes"`
}

// Candidate is one legal decode hypothesis. When evidence cannot uniquely
// decide (missing clock edge, repeated START, clock stretching, unknown SPI
// mode) all legal candidates are kept, each with confidence and reason.
type Candidate struct {
	ID           string        `json:"id"`
	Protocol     string        `json:"protocol"`
	Reason       string        `json:"reason"` // machine-readable cause
	Confidence   float64       `json:"confidence"`
	Hypothesis   string        `json:"hypothesis"` // e.g. "spi_mode=2", "inferred_bit=1"
	Transactions []Transaction `json:"transactions"`
	Diagnostics  []Diagnostic  `json:"diagnostics,omitempty"`
}

// DecodeResult is what a decoder returns: candidates plus diagnostics, or a
// terminal Failure. Equivalent inputs must yield a stable candidate order.
type DecodeResult struct {
	Protocol    string       `json:"protocol"`
	Candidates  []Candidate  `json:"candidates,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
	Failure     *Failure     `json:"failure,omitempty"`
}

// SortCandidates gives equivalent inputs a stable order: confidence desc,
// then ID asc.
func SortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].ID < cs[j].ID
	})
}

// ---------- event / decision layer ----------

// Event is a machine-readable record of everything that changes state.
// Judgment rules (keep candidates on missing clock edge / repeated START /
// clock stretching / unknown SPI mode; never stitch across data gaps) are
// themselves emitted as events.
type Event struct {
	ID      int64           `json:"id"`
	Seq     int             `json:"seq"`
	Type    string          `json:"type"` // import|decode|param_revision|source_patch|decision|rule|revert|export|import_bundle
	Payload json.RawMessage `json:"payload"`
	Parent  int64           `json:"parent"`
}

// Decision is a human verdict on a candidate. Conflicting decisions are all
// kept; the latest one is "effective" but history is never overwritten.
type Decision struct {
	ID          string `json:"id"`
	RunID       string `json:"run_id"`
	CandidateID string `json:"candidate_id"`
	Verdict     string `json:"verdict"` // "accepted" | "rejected"
	Rationale   string `json:"rationale"`
	Author      string `json:"author"`
	EventSeq    int    `json:"event_seq"`
}

// Run is one decode execution with full provenance.
type Run struct {
	ID             string         `json:"id"`
	CaptureID      string         `json:"capture_id"`
	CaptureDigest  string         `json:"capture_digest"`
	Params         Params         `json:"params"`
	ParamsDigest   string         `json:"params_digest"`
	DecoderVersion string         `json:"decoder_version"`
	ParentRunID    string         `json:"parent_run_id,omitempty"`
	Protocols      []string       `json:"protocols"`
	Result         *DecodeSummary `json:"result,omitempty"`
	Failure        *Failure       `json:"failure,omitempty"`
	EventSeq       int            `json:"event_seq"`
}

// DecodeSummary is the persisted, per-protocol decode outcome of a run.
type DecodeSummary struct {
	ByProtocol map[string]DecodeResult `json:"by_protocol"`
}

// Package model defines the domain types for the edge-decode workbench.
// Layering rule: captures, channels, edges and parameter sets are immutable
// source material; decode runs, candidates and decisions are derived and
// every derived object records its input digest, parameter version and
// parent dependencies.
package model

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// Channel roles used by the decoders.
const (
	RoleNone = "none"
	RoleSCL  = "scl"
	RoleSDA  = "sda"
	RoleSCLK = "sclk"
	RoleMOSI = "mosi"
	RoleMISO = "miso"
	RoleCS   = "cs"
	RoleCLK  = "clk"
	RoleDATA = "data"
)

// Run statuses. Budget exhaustion, corrupt input and semantic dead-ends are
// three distinct results, never collapsed into one generic failure.
const (
	RunOK              = "ok"
	RunAmbiguous       = "ambiguous"
	RunBudgetExhausted = "budget_exhausted"
	RunCorrupt         = "corrupt"
	RunUnsolvable      = "unsolvable"
)

// Transaction statuses.
const (
	TxnOK            = "ok"
	TxnGapTerminated = "gap_terminated" // data gap: never stitched into one transaction
	TxnIncomplete    = "incomplete"
)

// Diagnostic codes (machine readable judgement basis).
const (
	DiagClockStretch   = "clock_stretch"
	DiagClockMissing   = "clock_missing"
	DiagBoundarySample = "boundary_sample"
	DiagDataGap        = "data_gap"
	DiagRepeatedStart  = "repeated_start"
	DiagModeUnknown    = "spi_mode_unknown"
	DiagMultiCS        = "multi_chip_select"
	DiagBudget         = "budget_exhausted"
	DiagCorrupt        = "corrupt_input"
	DiagUnsolvable     = "unsolvable"
	DiagFork           = "candidate_fork"
)

type Channel struct {
	Index   int    `json:"index"`
	Alias   string `json:"alias"`
	Role    string `json:"role"`
	Invert  bool   `json:"invert"`
	Initial int    `json:"initial"` // raw level before the first edge, 0 or 1
}

type Edge struct {
	Channel int   `json:"channel"`
	TimeNs  int64 `json:"time_ns"`
	Level   int   `json:"level"` // raw post-transition level, 0 or 1
}

// Capture is immutable source material. Amendments create a new Capture with
// ParentID pointing at the previous version; the original is never mutated.
type Capture struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	SampleRateHz int64     `json:"sample_rate_hz"`
	Channels     []Channel `json:"channels"`
	Edges        []Edge    `json:"edges"`
	ParentID     string    `json:"parent_id,omitempty"`
	Note         string    `json:"note,omitempty"`
}

// CanonicalEdges returns edges in the deterministic order used for decoding
// and digests: (time, channel, original index). Equivalent inputs therefore
// produce stable ordering.
func (c *Capture) CanonicalEdges() []Edge {
	type indexed struct {
		e   Edge
		idx int
	}
	buf := make([]indexed, len(c.Edges))
	for i, e := range c.Edges {
		buf[i] = indexed{e, i}
	}
	sort.SliceStable(buf, func(i, j int) bool {
		a, b := buf[i], buf[j]
		if a.e.TimeNs != b.e.TimeNs {
			return a.e.TimeNs < b.e.TimeNs
		}
		if a.e.Channel != b.e.Channel {
			return a.e.Channel < b.e.Channel
		}
		return a.idx < b.idx
	})
	out := make([]Edge, len(buf))
	for i, b := range buf {
		out[i] = b.e
	}
	return out
}

// Digest is the stable identity of the immutable source content.
func (c *Capture) Digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "rate=%d\n", c.SampleRateHz)
	chs := append([]Channel(nil), c.Channels...)
	sort.Slice(chs, func(i, j int) bool { return chs[i].Index < chs[j].Index })
	for _, ch := range chs {
		fmt.Fprintf(h, "ch %d %s %s %v %d\n", ch.Index, ch.Alias, ch.Role, ch.Invert, ch.Initial)
	}
	for _, e := range c.CanonicalEdges() {
		fmt.Fprintf(h, "e %d %d %d\n", e.Channel, e.TimeNs, e.Level)
	}
	return fmt.Sprintf("%x", h.Sum(nil))[:16]
}

type I2CParams struct {
	StretchThresholdNs int64 `json:"stretch_threshold_ns"`
	GapThresholdNs     int64 `json:"gap_threshold_ns"`
	SetupMarginNs      int64 `json:"setup_margin_ns"`
	MaxMissedClocks    int   `json:"max_missed_clocks"`
}

type SPIParams struct {
	Mode           string `json:"mode"` // "unknown" or "0".."3"
	CSActiveLow    bool   `json:"cs_active_low"`
	SetupMarginNs  int64  `json:"setup_margin_ns"`
	GapThresholdNs int64  `json:"gap_threshold_ns"`
}

type SyncParams struct {
	Polarity       string `json:"polarity"` // "rising" or "falling"
	WordBits       int    `json:"word_bits"`
	GapThresholdNs int64  `json:"gap_threshold_ns"`
}

// ParamSet is an immutable parameter version. Revisions create a new version
// with ParentID pointing at its predecessor.
type ParamSet struct {
	ID             string     `json:"id"`
	Version        int        `json:"version"`
	ParentID       string     `json:"parent_id,omitempty"`
	DecoderVersion string     `json:"decoder_version"`
	Budget         int        `json:"budget"` // max candidates per run
	I2C            I2CParams  `json:"i2c"`
	SPI            SPIParams  `json:"spi"`
	Sync           SyncParams `json:"sync"`
	Note           string     `json:"note,omitempty"`
}

func (p *ParamSet) Digest() string {
	b, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}

type Diag struct {
	Severity string `json:"severity"` // error | warning | info
	Code     string `json:"code"`
	Message  string `json:"message"`
	Channel  int    `json:"channel"`  // -1 when not channel specific
	TimeNs   int64  `json:"time_ns"`  // -1 when not time specific
	EdgeIdx  int    `json:"edge_idx"` // -1 when not edge specific
}

type Byte struct {
	Seq        int     `json:"seq"`
	Value      int     `json:"value"`
	Ack        string  `json:"ack"` // ACK | NACK | none
	Dir        string  `json:"dir"` // i2c: "" ; spi: MOSI | MISO
	StartNs    int64   `json:"start_ns"`
	EndNs      int64   `json:"end_ns"`
	EdgeRefs   []int   `json:"edge_refs"` // indices into the capture edge list
	Confidence float64 `json:"confidence"`
	Note       string  `json:"note,omitempty"`
}

type Transaction struct {
	Seq           int    `json:"seq"`
	Kind          string `json:"kind"` // i2c_write | i2c_read | spi_transfer | sync_word
	CS            string `json:"cs,omitempty"`
	Addr          int    `json:"addr,omitempty"`
	StartNs       int64  `json:"start_ns"`
	EndNs         int64  `json:"end_ns"`
	Status        string `json:"status"`
	InvalidReason string `json:"invalid_reason,omitempty"`
	Bytes         []Byte `json:"bytes"`
}

type Candidate struct {
	Seq          int           `json:"seq"`
	Protocol     string        `json:"protocol"` // i2c | spi | sync
	Mode         string        `json:"mode,omitempty"`
	Confidence   float64       `json:"confidence"`
	Rationale    []string      `json:"rationale"`
	Transactions []Transaction `json:"transactions"`
	Diags        []Diag        `json:"diags"`
}

type RunResult struct {
	Status      string      `json:"status"`
	Candidates  []Candidate `json:"candidates"`
	Diags       []Diag      `json:"diags"` // run-level diagnostics
	InputDigest string      `json:"input_digest"`
}

// Decision is a mutable review judgement. Conflicting decisions are all kept;
// the visible conclusion is derived by folding decisions in event order.
type Decision struct {
	ID         string `json:"id"`
	EventSeq   int64  `json:"event_seq"`
	TargetKind string `json:"target_kind"` // candidate | transaction | byte
	TargetID   string `json:"target_id"`
	Action     string `json:"action"` // accept | reject | adjust
	Note       string `json:"note"`
	Actor      string `json:"actor"`
}

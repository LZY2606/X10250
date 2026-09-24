// Package model defines the domain types for the edge-decoding workbench.
//
// Layering rule: materials (captures, edges, probe config) are immutable and
// content-addressed; decisions and parameter revisions are events. Derived
// artifacts (decode runs) record input digest, parameter version and parents.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// Immutable materials
// ---------------------------------------------------------------------------

// SampleRate keeps the raw imported representation plus the normalized value.
type SampleRate struct {
	Raw string  `json:"raw"` // original representation, e.g. "1MHz" or "1000000"
	Hz  float64 `json:"hz"`  // normalized; 0 when unconvertible (diagnostic attached)
}

// Channel is one digital probe channel.
type Channel struct {
	ID       string `json:"id"`
	Alias    string `json:"alias"`    // 通道别名
	Role     string `json:"role"`     // scl,sda,sclk,mosi,miso,cs,data,clock
	Inverted bool   `json:"inverted"` // 电平反相 (applied at decode time, raw kept)
}

// Edge is one digital transition. TimeRaw preserves the imported text;
// Sample is the normalized sample index. Level is the raw level (0/1)
// BEFORE channel inversion is applied.
type Edge struct {
	Seq       int    `json:"seq"`
	ChannelID string `json:"channel"`
	TimeRaw   string `json:"time_raw"` // original representation (unit carried by capture.TimeUnit)
	Sample    int64  `json:"sample"`   // normalized sample index; -1 when unconvertible
	Level     int    `json:"level"`    // raw level after transition: 0 or 1
}

// Capture is an immutable imported material: edges + probe configuration.
type Capture struct {
	ID          string     `json:"id"` // content hash
	ProjectID   string     `json:"project_id"`
	Name        string     `json:"name"`
	ParentID    string     `json:"parent_id,omitempty"` // set for source-change versions
	ChangeNote  string     `json:"change_note,omitempty"`
	TimeUnit    string     `json:"time_unit"` // unit of Edge.TimeRaw, e.g. "sample","us","ns"
	SampleRate  SampleRate `json:"sample_rate"`
	Channels    []Channel  `json:"channels"`
	Edges       []Edge     `json:"edges"`
	Diagnostics []Diag     `json:"diagnostics,omitempty"` // import-time diagnostics (unconvertible values etc.)
	Source      string     `json:"source"`                // import provenance
}

// Diag is a positioned diagnostic. Codes distinguish the three failure
// classes: resource_budget_exhausted, format_corrupt, semantic_unsolvable,
// plus decode observations (data_gap, missing_clock_edges, ...).
type Diag struct {
	Severity string `json:"severity"` // error|warning
	Code     string `json:"code"`
	Position string `json:"position"` // edge ref or sample, e.g. "edge[12]" or "sample@4500"
	Message  string `json:"message"`
}

const (
	DiagBudgetExhausted  = "resource_budget_exhausted"
	DiagFormatCorrupt    = "format_corrupt"
	DiagSemanticUnsolv   = "semantic_unsolvable"
	DiagDataGap          = "data_gap"
	DiagMissingClock     = "missing_clock_edges"
	DiagClockStretch     = "clock_stretch"
	DiagRepeatedStart    = "repeated_start"
	DiagAmbiguousSPIMode = "ambiguous_spi_mode"
	DiagUnconvertible    = "unconvertible_value"
)

// ---------------------------------------------------------------------------
// Decode parameters (versioned, modifiable via events)
// ---------------------------------------------------------------------------

// Params is one versioned decoder-parameter set for a capture.
type Params struct {
	ID             string   `json:"id"` // content hash
	CaptureID      string   `json:"capture_id"`
	Version        int      `json:"version"`
	Decoder        string   `json:"decoder"` // i2c | spi | sync
	ClockChannel   string   `json:"clock_channel,omitempty"`
	DataChannel    string   `json:"data_channel,omitempty"`  // sda / mosi / data
	Data2Channel   string   `json:"data2_channel,omitempty"` // miso
	CSChannels     []string `json:"cs_channels,omitempty"`
	ClockPolarity  int      `json:"clock_polarity"`     // idle level of clock (CPOL)
	SampleEdge     string   `json:"sample_edge"`        // rising|falling (boundary sampling)
	SPIMode        string   `json:"spi_mode,omitempty"` // "auto" keeps parallel candidates
	BitOrder       string   `json:"bit_order"`          // msb|lsb
	GapSamples     int64    `json:"gap_samples"`        // data-gap threshold (no splice across)
	StretchSamples int64    `json:"stretch_samples"`    // clock-stretch threshold
	FrameBits      int      `json:"frame_bits"`         // sync frame width (default 8)
	MaxEdges       int      `json:"max_edges"`          // resource budget
	DecoderVersion string   `json:"decoder_version"`    // 解码器版本, part of identity
}

// ---------------------------------------------------------------------------
// Decode artifacts
// ---------------------------------------------------------------------------

// EdgeRef points back to a concrete edge in a capture (byte -> edge link).
type EdgeRef struct {
	ChannelID string `json:"channel"`
	Sample    int64  `json:"sample"`
}

// Byte is one decoded byte with provenance to its sampling edges.
type Byte struct {
	ID       string    `json:"id"` // stable content id
	Value    int       `json:"value"`
	Ack      *bool     `json:"ack,omitempty"` // i2c: true=ACK false=NACK
	EdgeRefs []EdgeRef `json:"edge_refs"`
	BitCount int       `json:"bit_count"`
}

// Transaction is one decoded bus transaction.
type Transaction struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"` // i2c-write|i2c-read|spi-transfer|sync-frame
	CSChannel   string   `json:"cs_channel,omitempty"`
	Address     int      `json:"address,omitempty"`
	Bytes       []Byte   `json:"bytes"`
	StartSample int64    `json:"start_sample"`
	EndSample   int64    `json:"end_sample"`
	Flags       []string `json:"flags,omitempty"` // repeated-start, clock-stretch, truncated-gap
}

// Candidate is one decode hypothesis. Ambiguous situations keep several.
type Candidate struct {
	ID           string        `json:"id"`
	Group        string        `json:"group"` // candidates in one group are mutually exclusive
	Hypothesis   string        `json:"hypothesis"`
	Confidence   float64       `json:"confidence"`
	Reasons      []string      `json:"reasons"`
	Transactions []Transaction `json:"transactions"`
	Diagnostics  []Diag        `json:"diagnostics,omitempty"`
}

// Run is an immutable decode artifact keyed by (capture, params, decoder ver).
type Run struct {
	ID             string      `json:"id"`
	CaptureID      string      `json:"capture_id"`
	ParamsID       string      `json:"params_id"`
	ParamsVersion  int         `json:"params_version"`
	DecoderVersion string      `json:"decoder_version"`
	Status         string      `json:"status"` // ok | resource_budget_exhausted | format_corrupt | semantic_unsolvable
	Candidates     []Candidate `json:"candidates"`
	Diagnostics    []Diag      `json:"diagnostics,omitempty"`
}

const (
	StatusOK              = "ok"
	StatusBudgetExhausted = "resource_budget_exhausted"
	StatusFormatCorrupt   = "format_corrupt"
	StatusSemanticUnsolv  = "semantic_unsolvable"
)

// ---------------------------------------------------------------------------
// Events (the decision / provenance log)
// ---------------------------------------------------------------------------

type Event struct {
	ProjectID string          `json:"project_id"`
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"` // import_capture|source_change|param_revision|decode_run|decision|judgment|note
	Payload   json.RawMessage `json:"payload"`
	PrevHash  string          `json:"prev_hash"`
	Hash      string          `json:"hash"`
	CreatedAt string          `json:"created_at"`
}

const (
	EvImportCapture = "import_capture"
	EvSourceChange  = "source_change"
	EvParamRevision = "param_revision"
	EvDecodeRun     = "decode_run"
	EvDecision      = "decision"
	EvJudgment      = "judgment"
	EvNote          = "note"
)

type DecisionPayload struct {
	TargetGroup string `json:"target_group"`
	CandidateID string `json:"candidate_id"`
	Action      string `json:"action"` // accept | reject | adjust
	ParamsDelta string `json:"params_delta,omitempty"`
	Rationale   string `json:"rationale"`
	Author      string `json:"author"`
}

// JudgmentPayload is the machine-readable form of review rules, e.g.
// "retain multiple candidates when clock edges are missing / repeated START /
// clock stretching / SPI mode unknown" and "never splice across data gaps".
type JudgmentPayload struct {
	Rule       string   `json:"rule"` // retain_multiple_candidates | no_splice_across_data_gap
	Conditions []string `json:"conditions"`
	Applied    bool     `json:"applied"`
	Scope      []string `json:"scope"` // affected transaction/candidate ids
	Note       string   `json:"note"`
}

// ---------------------------------------------------------------------------
// Hashing / canonical JSON
// ---------------------------------------------------------------------------

// Canonical marshals with sorted keys and no whitespace so hashes are stable.
func Canonical(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ComputeCaptureID derives the stable identity of a capture from its content
// (identity is independent of project so export/import keeps it stable).
func ComputeCaptureID(c *Capture) string {
	type wire struct {
		Name       string     `json:"name"`
		ParentID   string     `json:"parent_id,omitempty"`
		ChangeNote string     `json:"change_note,omitempty"`
		TimeUnit   string     `json:"time_unit"`
		SampleRate SampleRate `json:"sample_rate"`
		Channels   []Channel  `json:"channels"`
		Edges      []Edge     `json:"edges"`
	}
	return HashBytes(Canonical(wire{c.Name, c.ParentID, c.ChangeNote, c.TimeUnit, c.SampleRate, c.Channels, c.Edges}))
}

func ComputeParamsID(p *Params) string {
	type wire struct {
		CaptureID      string   `json:"capture_id"`
		Version        int      `json:"version"`
		Decoder        string   `json:"decoder"`
		ClockChannel   string   `json:"clock_channel,omitempty"`
		DataChannel    string   `json:"data_channel,omitempty"`
		Data2Channel   string   `json:"data2_channel,omitempty"`
		CSChannels     []string `json:"cs_channels,omitempty"`
		ClockPolarity  int      `json:"clock_polarity"`
		SampleEdge     string   `json:"sample_edge"`
		SPIMode        string   `json:"spi_mode,omitempty"`
		BitOrder       string   `json:"bit_order"`
		GapSamples     int64    `json:"gap_samples"`
		StretchSamples int64    `json:"stretch_samples"`
		FrameBits      int      `json:"frame_bits"`
		MaxEdges       int      `json:"max_edges"`
		DecoderVersion string   `json:"decoder_version"`
	}
	return HashBytes(Canonical(wire{p.CaptureID, p.Version, p.Decoder, p.ClockChannel, p.DataChannel, p.Data2Channel, p.CSChannels, p.ClockPolarity, p.SampleEdge, p.SPIMode, p.BitOrder, p.GapSamples, p.StretchSamples, p.FrameBits, p.MaxEdges, p.DecoderVersion}))
}

func ComputeRunID(r *Run) string {
	type wire struct {
		CaptureID      string      `json:"capture_id"`
		ParamsID       string      `json:"params_id"`
		DecoderVersion string      `json:"decoder_version"`
		Status         string      `json:"status"`
		Candidates     []Candidate `json:"candidates"`
		Diagnostics    []Diag      `json:"diagnostics,omitempty"`
	}
	return HashBytes(Canonical(wire{r.CaptureID, r.ParamsID, r.DecoderVersion, r.Status, r.Candidates, r.Diagnostics}))
}

// ComputeEventHash chains events: hash covers kind, payload and prev hash.
func ComputeEventHash(projectID string, seq int64, kind string, payload json.RawMessage, prevHash string) string {
	return HashBytes([]byte(fmt.Sprintf("%s|%d|%s|%s|%s", projectID, seq, kind, string(payload), prevHash)))
}

// SortEdges orders edges deterministically: equivalent inputs give stable order.
func SortEdges(edges []Edge) {
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].Sample != edges[j].Sample {
			return edges[i].Sample < edges[j].Sample
		}
		if edges[i].ChannelID != edges[j].ChannelID {
			return edges[i].ChannelID < edges[j].ChannelID
		}
		return edges[i].Seq < edges[j].Seq
	})
}

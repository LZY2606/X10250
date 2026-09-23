// Package decode translates immutable edge material into reviewable bus
// transactions. Ambiguity never collapses silently: missing clock edges,
// repeated START, clock stretching and unknown SPI mode keep multiple decode
// candidates; data gaps are never stitched into a transaction.
package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// Result statuses. Budget exhaustion, corrupt format and semantic
// unsolvability are three distinct outcomes, never merged.
const (
	StatusOK       = "ok"
	StatusBudget   = "budget_exhausted"
	StatusCorrupt  = "corrupt_format"
	StatusUnsolvab = "semantic_unsolvable"
)

// Reason codes attached to candidates and emitted as machine-readable events.
const (
	ReasonMissingClock   = "missing_clock_edge"
	ReasonRepeatedStart  = "repeated_start"
	ReasonClockStretch   = "clock_stretching"
	ReasonDataGap        = "data_gap"
	ReasonSPIModeUnknown = "spi_mode_unknown"
	ReasonBoundarySample = "boundary_sampling"
	ReasonMultiCS        = "multi_device_cs"
	ReasonClockPolarity  = "clock_polarity"
)

// Params is the derived-layer parameter set. Every run records its parameter
// version so conclusions can be traced back to the exact rule version.
type Params struct {
	DecoderVersion      string `json:"decoder_version"`
	Protocol            string `json:"protocol"` // auto|i2c|spi|sync
	ClockPolarity       string `json:"clock_polarity"`
	SPIMode             string `json:"spi_mode"` // unknown|0|1|2|3
	MaxEdges            int    `json:"max_edges"`
	MaxCandidates       int    `json:"max_candidates"`
	GapThresholdNS      int64  `json:"gap_threshold_ns"`
	BoundaryToleranceNS int64  `json:"boundary_tolerance_ns"`
	StretchThresholdNS  int64  `json:"stretch_threshold_ns"`
}

// DefaultParams returns the baseline parameter set.
func DefaultParams() Params {
	return Params{
		DecoderVersion:      "edge-decode/1.0.0",
		Protocol:            "auto",
		ClockPolarity:       "rising",
		SPIMode:             "unknown",
		MaxEdges:            100000,
		MaxCandidates:       64,
		GapThresholdNS:      5000,
		BoundaryToleranceNS: 40,
		StretchThresholdNS:  3000,
	}
}

// Version is a short digest of the parameter set, recorded on every run.
func (p Params) Version() string { return model.Digest(p)[:12] }

// Diag is a positioned diagnostic. Incomplete input yields diagnostics with
// positions; nothing is faked with empty or default values.
type Diag struct {
	Position string `json:"position"` // edge index or time coordinate
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity"` // error|warning|info
}

// Byte is one decoded byte with its edge provenance in the capture
// coordinate basis (edge indexes and nanosecond timestamps).
type Byte struct {
	Value     int   `json:"value"`
	Ack       *bool `json:"ack,omitempty"` // I2C only: true=ACK, false=NACK
	EdgeStart int   `json:"edge_start"`
	EdgeEnd   int   `json:"edge_end"`
	StartNS   int64 `json:"start_ns"`
	EndNS     int64 `json:"end_ns"`
}

// Transaction groups bytes that belong to one bus transaction. Bytes across
// a data gap are never stitched into one transaction.
type Transaction struct {
	Device    string `json:"device"` // chip-select alias or I2C address
	Kind      string `json:"kind"`   // write|read|transfer
	ByteStart int    `json:"byte_start"`
	ByteEnd   int    `json:"byte_end"` // exclusive
	EdgeStart int    `json:"edge_start"`
	EdgeEnd   int    `json:"edge_end"`
	GapBefore bool   `json:"gap_before"`
}

// Candidate is one decode hypothesis kept alive until a human decision.
type Candidate struct {
	ID         string        `json:"id"`
	Protocol   string        `json:"protocol"`
	Rank       int           `json:"rank"`
	Confidence float64       `json:"confidence"`
	Reasons    []string      `json:"reasons"`
	Detail     string        `json:"detail"`
	StartEdge  int           `json:"start_edge"`
	EndEdge    int           `json:"end_edge"`
	Bytes      []Byte        `json:"bytes"`
	Txns       []Transaction `json:"txns"`
}

// Result is the outcome of one decode run.
type Result struct {
	Status      string      `json:"status"`
	Diagnostics []Diag      `json:"diagnostics"`
	Candidates  []Candidate `json:"candidates"`
}

// Run executes decoding over immutable edge material. Equivalent inputs
// produce identical candidate identities and ordering.
func Run(channels []model.Channel, edges []model.Edge, p Params) Result {
	if p.MaxEdges <= 0 {
		p.MaxEdges = 1
	}
	if len(edges) > p.MaxEdges {
		return Result{Status: StatusBudget, Diagnostics: []Diag{{
			Position: fmt.Sprintf("edge[%d]", p.MaxEdges),
			Code:     "budget_exhausted",
			Message:  fmt.Sprintf("edge count %d exceeds budget %d", len(edges), p.MaxEdges),
			Severity: "error",
		}}}
	}
	if diags := validate(edges); len(diags) > 0 {
		return Result{Status: StatusCorrupt, Diagnostics: diags}
	}

	inv := inversion(channels)
	levels := make([]int, len(edges))
	for i, e := range edges {
		lv := e.Level
		if inv[e.Channel] {
			lv = 1 - lv
		}
		levels[i] = lv
	}

	var cands []Candidate
	var diags []Diag
	protos := protocols(p, channels)
	for _, proto := range protos {
		var r Result
		switch proto {
		case "i2c":
			r = decodeI2C(channels, edges, levels, p)
		case "spi":
			r = decodeSPI(channels, edges, levels, p)
		case "sync":
			r = decodeSync(channels, edges, levels, p)
		}
		diags = append(diags, r.Diagnostics...)
		cands = append(cands, r.Candidates...)
	}

	if len(cands) > p.MaxCandidates {
		return Result{Status: StatusBudget, Diagnostics: append(diags, Diag{
			Position: "candidates",
			Code:     "budget_exhausted",
			Message:  fmt.Sprintf("candidate count %d exceeds budget %d", len(cands), p.MaxCandidates),
			Severity: "error",
		})}
	}
	if len(cands) == 0 {
		return Result{Status: StatusUnsolvab, Diagnostics: append(diags, Diag{
			Position: "capture",
			Code:     "no_legal_candidate",
			Message:  "no legal decode candidate exists for the given probe roles and parameters",
			Severity: "error",
		})}
	}

	// Stable ordering: confidence desc, protocol asc, content id asc.
	for i := range cands {
		cands[i].ID = model.ID("cand", struct {
			P string        `json:"p"`
			R []string      `json:"r"`
			D string        `json:"d"`
			S int           `json:"s"`
			E int           `json:"e"`
			B []Byte        `json:"b"`
			T []Transaction `json:"t"`
		}{cands[i].Protocol, cands[i].Reasons, cands[i].Detail, cands[i].StartEdge, cands[i].EndEdge, cands[i].Bytes, cands[i].Txns})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		if cands[i].Protocol != cands[j].Protocol {
			return cands[i].Protocol < cands[j].Protocol
		}
		return cands[i].ID < cands[j].ID
	})
	for i := range cands {
		cands[i].Rank = i
	}
	return Result{Status: StatusOK, Diagnostics: diags, Candidates: cands}
}

func validate(edges []model.Edge) []Diag {
	var diags []Diag
	last := map[string]int64{}
	for i, e := range edges {
		if !e.TimeOK {
			diags = append(diags, Diag{
				Position: fmt.Sprintf("edge[%d]", e.Idx),
				Code:     "unconvertible_time",
				Message:  fmt.Sprintf("time literal %q has no unit basis convertible to ns", e.TimeRaw),
				Severity: "error",
			})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, Diag{
				Position: fmt.Sprintf("edge[%d]", e.Idx),
				Code:     "bad_level",
				Message:  fmt.Sprintf("level %d is not a digital level", e.Level),
				Severity: "error",
			})
		}
		if t, ok := last[e.Channel]; ok && e.TimeNS < t {
			diags = append(diags, Diag{
				Position: fmt.Sprintf("edge[%d]", e.Idx),
				Code:     "non_monotonic_time",
				Message:  fmt.Sprintf("channel %s time %dns goes backwards (previous %dns)", e.Channel, e.TimeNS, t),
				Severity: "error",
			})
		}
		last[e.Channel] = e.TimeNS
		_ = i
	}
	return diags
}

func inversion(channels []model.Channel) map[string]bool {
	m := map[string]bool{}
	for _, c := range channels {
		m[c.Alias] = c.Inverted
	}
	return m
}

func roleOf(channels []model.Channel, role string) []string {
	var out []string
	for _, c := range channels {
		if c.Role == role {
			out = append(out, c.Alias)
		}
	}
	return out
}

func protocols(p Params, channels []model.Channel) []string {
	if p.Protocol != "" && p.Protocol != "auto" {
		return []string{p.Protocol}
	}
	var out []string
	has := func(role string) bool { return len(roleOf(channels, role)) > 0 }
	if has("sda") && has("scl") {
		out = append(out, "i2c")
	}
	if has("sclk") {
		out = append(out, "spi")
	}
	if has("clock") && has("data") {
		out = append(out, "sync")
	}
	return out
}

// sortedReasons normalizes reason sets so equivalent hypotheses get equal ids.
func sortedReasons(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

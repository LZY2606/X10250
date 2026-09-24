// Package decode turns normalized edge captures into protocol transactions.
// Ambiguity is first-class: missing clock edges, unknown SPI mode, repeated
// START, clock stretching and boundary sampling all produce multiple
// candidates instead of silent guesses. Data gaps are never stitched into
// one transaction.
package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/edge"
)

// Status is the structured result of a decode run. Budget exhaustion,
// corrupt format (import layer) and semantic dead-ends are distinct.
type Status string

const (
	StatusOK              Status = "ok"
	StatusBudgetExhausted Status = "budget_exhausted"
	StatusUnsolvable      Status = "semantic_unsolvable"
)

// Params is one versioned parameter set. Version bumps on every revision;
// decode results record it so history is replayable.
type Params struct {
	Version            int               `json:"version"`
	Protocol           string            `json:"protocol"` // i2c | spi | sync
	ClockPolarity      string            `json:"clock_polarity"`
	BoundaryWindowNs   int64             `json:"boundary_window_ns"`
	StretchThresholdNs int64             `json:"stretch_threshold_ns"`
	GapThresholdNs     int64             `json:"gap_threshold_ns"`
	SPIMode            string            `json:"spi_mode"` // "auto" | "0".."3"
	MaxCandidates      int               `json:"max_candidates"`
	MaxEdges           int               `json:"max_edges"`
	DecoderVersion     string            `json:"decoder_version"`
	Roles              map[string]string `json:"roles"` // role -> channel id
}

// DefaultParams derives a v1 parameter set from the probe configuration.
func DefaultParams(cap *edge.Capture, protocol string, roles map[string]string) Params {
	return Params{
		Version:            1,
		Protocol:           protocol,
		ClockPolarity:      cap.Probe.ClockPolarity,
		BoundaryWindowNs:   40,
		StretchThresholdNs: 5000,
		GapThresholdNs:     100000,
		SPIMode:            "auto",
		MaxCandidates:      16,
		MaxEdges:           100000,
		DecoderVersion:     cap.Probe.DecoderVersion,
		Roles:              roles,
	}
}

// Byte is one decoded byte linked back to the edges that produced it.
type Byte struct {
	Value   int      `json:"value"`
	Dir     string   `json:"dir,omitempty"` // "mosi" | "miso" (spi); empty elsewhere
	Ack     string   `json:"ack,omitempty"` // "ack" | "nack" (i2c 9th bit)
	TNs     int64    `json:"t_ns"`
	EdgeIDs []string `json:"edge_ids"`
}

// Marker is a structural event shared by all candidates of a transaction.
type Marker struct {
	Kind string `json:"kind"` // start|repeated-start|stop|stretch|gap|cs-assert|cs-release
	TNs  int64  `json:"t_ns"`
}

// Candidate is one legal decoding of a transaction.
type Candidate struct {
	ID         string  `json:"id"`
	Label      string  `json:"label"`
	Bytes      []Byte  `json:"bytes"`
	Confidence float64 `json:"confidence"`
	Notes      []string `json:"notes,omitempty"`
	State      string  `json:"state"` // proposed | accepted | rejected
}

// Transaction is a bus transaction. Bytes live inside candidates; the
// transaction carries structure (markers, span, device) and never merges
// across a data gap.
type Transaction struct {
	ID          string      `json:"id"`
	Protocol    string      `json:"protocol"`
	Device      string      `json:"device"`
	StartNs     int64       `json:"start_ns"`
	EndNs       int64       `json:"end_ns"`
	Markers     []Marker    `json:"markers"`
	Candidates  []Candidate `json:"candidates"`
	GapAfter    bool        `json:"gap_after,omitempty"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
}

// Result is the full decode output.
type Result struct {
	Status      Status            `json:"status"`
	Diagnostics []edge.Diagnostic `json:"diagnostics,omitempty"`
	Transactions []Transaction    `json:"transactions,omitempty"`
	ParamsHash  string            `json:"params_hash"`
	InputHash   string            `json:"input_hash"`
}

// Decode dispatches on protocol and enforces budgets.
func Decode(cap *edge.Capture, p Params) Result {
	res := Result{Status: StatusOK, ParamsHash: edge.Hash(edge.CanonicalJSON(p)), InputHash: cap.ArtifactID}
	if len(cap.Edges) > p.MaxEdges {
		res.Status = StatusBudgetExhausted
		res.Diagnostics = append(res.Diagnostics, edge.Diagnostic{
			Pos: fmt.Sprintf("/edges[0..%d]", len(cap.Edges)-1), Code: "budget-edges",
			Message: fmt.Sprintf("%d edges exceed budget %d", len(cap.Edges), p.MaxEdges)})
		return res
	}
	var out Result
	switch p.Protocol {
	case "i2c":
		out = decodeI2C(cap, p)
	case "spi":
		out = decodeSPI(cap, p)
	case "sync":
		out = decodeSync(cap, p)
	default:
		out = Result{Status: StatusUnsolvable, Diagnostics: []edge.Diagnostic{
			{Pos: "/protocol", Code: "unknown-protocol", Message: "no decoder for " + p.Protocol}}}
	}
	out.ParamsHash = res.ParamsHash
	out.InputHash = res.InputHash
	return out
}

// budget guards candidate explosion.
type budget struct{ max, used int }

func (b *budget) take() bool {
	b.used++
	return b.used <= b.max
}

// finishCandidates sorts deterministically: confidence desc, then label, so
// equivalent inputs always yield a stable order.
func finishCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Confidence != cs[j].Confidence {
			return cs[i].Confidence > cs[j].Confidence
		}
		return cs[i].Label < cs[j].Label
	})
	for i := range cs {
		cs[i].ID = fmt.Sprintf("cand-%02d", i+1)
		if cs[i].State == "" {
			cs[i].State = "proposed"
		}
	}
}

func clampConf(c float64) float64 {
	if c < 0.05 {
		return 0.05
	}
	if c > 1 {
		return 1
	}
	return float64(int(c*100+0.5)) / 100
}

// stream returns edges for the given channels, already time-sorted.
func stream(cap *edge.Capture, chs ...string) []edge.Edge {
	want := map[string]bool{}
	for _, c := range chs {
		want[c] = true
	}
	var out []edge.Edge
	for _, e := range cap.Edges {
		if want[e.Channel] {
			out = append(out, e)
		}
	}
	return out
}

// effLevel applies inversion to a raw level.
func effLevel(cap *edge.Capture, ch string, lvl int) int {
	if cap.Inverted[ch] {
		return 1 - lvl
	}
	return lvl
}

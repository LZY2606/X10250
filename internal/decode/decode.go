// Package decode translates raw digital channel edges into reviewable
// I2C, SPI and custom synchronous transactions.
//
// Hard rules implemented here:
//   - missing clock edges, repeated START, clock stretching and unknown
//     SPI mode keep multiple decode candidates;
//   - data gaps are never stitched into a single transaction;
//   - budget exhaustion, corrupt input and semantic no-solution are three
//     distinct run results;
//   - equivalent inputs produce identical candidate/byte ordering.
package decode

import (
	"fmt"
	"sort"
	"strings"

	"edgedesk/internal/model"
)

// Input is the normalized decode input: probe config plus raw edges.
type Input struct {
	Channels []model.Channel
	Edges    map[string][]model.Edge // raw levels, keyed by channel ID
}

// nedge is an edge after level inversion (derived layer).
type nedge struct {
	id    string
	t     int64
	level int
}

// bitCell is one sampled bit; amb marks a boundary sample whose value
// cannot be uniquely decided.
type bitCell struct {
	v       int
	amb     bool
	t       int64
	edgeID  string // clock edge that sampled this bit
}

// Byte is one decoded byte/word inside a transaction.
type Byte struct {
	Value         int
	HasValue      bool
	Ack           string
	Valid         bool
	InvalidReason string
	FirstEdgeID   string
	LastEdgeID    string
}

// Txn is one decoded transaction.
type Txn struct {
	Kind    string
	Device  string
	StartPs int64
	EndPs   int64
	Meta    map[string]string
	Bytes   []Byte
}

// Candidate is one legal interpretation of the capture for one bus.
type Candidate struct {
	Bus        string
	Note       string
	Confidence float64
	Txns       []Txn
}

// Result is the output of one decode run.
type Result struct {
	Status      string
	Diagnostics []model.Diagnostic
	Candidates  []Candidate
	Judgments   []model.Judgment
}

// Validate checks raw edge well-formedness. Any violation is a "corrupt"
// result with a positioned diagnostic.
func Validate(in Input) []model.Diagnostic {
	var diags []model.Diagnostic
	for _, ch := range in.Channels {
		edges := in.Edges[ch.ID]
		prev := int64(-1)
		for i, e := range edges {
			if e.Level != 0 && e.Level != 1 {
				diags = append(diags, model.Diagnostic{
					Code: "corrupt_level", ChannelID: ch.ID, EdgeID: e.ID,
					Tps: e.Tps, HasPos: true,
					Message: fmt.Sprintf("channel %q edge %d has level %d, want 0/1", ch.Alias, i, e.Level),
				})
			}
			if e.Tps <= prev {
				diags = append(diags, model.Diagnostic{
					Code: "corrupt_order", ChannelID: ch.ID, EdgeID: e.ID,
					Tps: e.Tps, HasPos: true,
					Message: fmt.Sprintf("channel %q edge %d at %d ps is not after previous edge", ch.Alias, i, e.Tps),
				})
			}
			prev = e.Tps
		}
	}
	return diags
}

// Run executes one full decode pass.
func Run(in Input, p model.ParamSet) Result {
	res := Result{Status: model.RunOK}

	// 1. corrupt input
	if diags := Validate(in); len(diags) > 0 {
		res.Status = model.RunCorrupt
		res.Diagnostics = diags
		return res
	}

	// 2. resource budget
	total := 0
	for _, ch := range in.Channels {
		total += len(in.Edges[ch.ID])
		if total > p.MaxEdges {
			last := in.Edges[ch.ID][len(in.Edges[ch.ID])-1]
			res.Status = model.RunBudgetExhausted
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: "budget_exhausted", ChannelID: ch.ID, EdgeID: last.ID,
				Tps: last.Tps, HasPos: true,
				Message: fmt.Sprintf("edge budget %d exceeded (%d edges so far)", p.MaxEdges, total),
			})
			return res
		}
	}

	// 3. per-bus decode
	byRole := map[string]model.Channel{}
	var csRoles []model.Channel
	for _, ch := range in.Channels {
		if strings.HasPrefix(ch.Role, "cs") {
			csRoles = append(csRoles, ch)
			continue
		}
		byRole[ch.Role] = ch
	}
	norm := func(ch model.Channel) []nedge {
		out := make([]nedge, 0, len(in.Edges[ch.ID]))
		for _, e := range in.Edges[ch.ID] {
			lv := e.Level
			if ch.Inverted {
				lv = 1 - lv
			}
			out = append(out, nedge{id: e.ID, t: e.Tps, level: lv})
		}
		return out
	}

	sawClock := false
	if sda, okS := byRole["sda"]; okS {
		if scl, okC := byRole["scl"]; okC {
			se := norm(scl)
			if len(se) > 0 {
				sawClock = true
			}
			cs, ds, js := decodeI2C(norm(sda), se, p)
			res.Candidates = append(res.Candidates, cs...)
			res.Diagnostics = append(res.Diagnostics, ds...)
			res.Judgments = append(res.Judgments, js...)
		}
	}
	if sclk, ok := byRole["sclk"]; ok {
		se := norm(sclk)
		if len(se) > 0 {
			sawClock = true
		}
		var mosi, miso []nedge
		if ch, ok := byRole["mosi"]; ok {
			mosi = norm(ch)
		}
		if ch, ok := byRole["miso"]; ok {
			miso = norm(ch)
		}
		var wins []csChannel
		for _, c := range csRoles {
			wins = append(wins, csChannel{alias: c.Alias, edges: norm(c)})
		}
		cs, ds, js := decodeSPI(se, mosi, miso, wins, p)
		res.Candidates = append(res.Candidates, cs...)
		res.Diagnostics = append(res.Diagnostics, ds...)
		res.Judgments = append(res.Judgments, js...)
	}
	if sclk, ok := byRole["sync_clk"]; ok {
		if sdat, ok2 := byRole["sync_data"]; ok2 {
			se := norm(sclk)
			if len(se) > 0 {
				sawClock = true
			}
			cs, ds, js := decodeSync(norm(sdat), se, sclk.ClockPolarity, p)
			res.Candidates = append(res.Candidates, cs...)
			res.Diagnostics = append(res.Diagnostics, ds...)
			res.Judgments = append(res.Judgments, js...)
		}
	}

	if len(res.Candidates) == 0 {
		res.Status = model.RunUnsolvable
		if !sawClock {
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: "no_clock_edges", Message: "no clock edges on any clock channel; no bus is decodable",
			})
		} else {
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: "no_decodable_bus", Message: "no bus produced a legal decode candidate",
			})
		}
		return res
	}

	// 4. stable ordering: confidence desc, then bus, then note.
	sort.SliceStable(res.Candidates, func(i, j int) bool {
		a, b := res.Candidates[i], res.Candidates[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Bus != b.Bus {
			return a.Bus < b.Bus
		}
		return a.Note < b.Note
	})
	if len(res.Candidates) > p.MaxCandidates {
		res.Candidates = res.Candidates[:p.MaxCandidates]
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code:    "candidate_budget_exhausted",
			Message: fmt.Sprintf("candidate budget %d exceeded; tail candidates dropped", p.MaxCandidates),
		})
	}
	return res
}

// expandAmb enumerates assignments for n ambiguous positions, most
// confident first (all-base first), capped by maxN.
func expandAmb(n, maxN int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	total := 1 << uint(n)
	if total > maxN {
		total = maxN
	}
	out := make([][]int, 0, total)
	for mask := 0; mask < total; mask++ {
		a := make([]int, n)
		for i := 0; i < n; i++ {
			a[i] = (mask >> uint(i)) & 1
		}
		out = append(out, a)
	}
	return out
}

// ambNear reports whether any data edge lies within +/-window of t.
func ambNear(data []nedge, t, window int64) bool {
	i := sort.Search(len(data), func(i int) bool { return data[i].t >= t-window })
	for ; i < len(data) && data[i].t <= t+window; i++ {
		return true
	}
	return false
}

// Package decode turns immutable edge captures into reviewable I2C, SPI and
// custom synchronous transactions. Ambiguous inputs produce multiple legal
// candidates; a data gap never stitches two fragments into one transaction.
package decode

import (
	"sort"

	"edgedesk/internal/model"
)

// ResultKind distinguishes the three terminal failure modes from success.
type ResultKind string

const (
	Ok               ResultKind = "ok"
	BudgetExhausted  ResultKind = "budget-exhausted"
	CorruptFormat    ResultKind = "corrupt-format"
	Unsolvable       ResultKind = "unsolvable"
)

// Budget caps work so a hostile import cannot exhaust the service.
type Budget struct {
	MaxEdges      int
	MaxCandidates int
}

// DefaultBudget is used by the server and case pack.
var DefaultBudget = Budget{MaxEdges: 200_000, MaxCandidates: 512}

// Diagnostic is a positioned finding. It never hides failure behind a default.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Channel string `json:"channel,omitempty"`
	EdgeID  string `json:"edge_id,omitempty"`
	TimeNs  int64  `json:"time_ns"`
}

// Bit records the sampled level and the exact sampling edge (byte -> edges).
type Bit struct {
	Value     int    `json:"value"`
	EdgeID    string `json:"edge_id"`
	TimeNs    int64  `json:"time_ns"`
	Stretch   bool   `json:"stretch,omitempty"`
	Ambiguous bool   `json:"ambiguous,omitempty"`
}

// Byte is one decoded word. Every byte traces back to concrete edge IDs.
type Byte struct {
	ID        string `json:"id"`
	Value     int    `json:"value"`
	Ack       *bool  `json:"ack,omitempty"` // I2C only: true=ACK, false=NACK
	Bits      []Bit  `json:"bits"`
	EdgeIDs   []string `json:"edge_ids"`
	StartNs   int64  `json:"start_ns"`
	EndNs     int64  `json:"end_ns"`
	Channel   string `json:"channel"`
	Device    string `json:"device,omitempty"`
	Partial   bool   `json:"partial,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Transaction groups bytes. Gap-terminated and repeated-start transfers are
// never merged with surrounding traffic.
type Transaction struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Decoder       string `json:"decoder"`
	Device        string `json:"device,omitempty"`
	Bytes         []Byte `json:"bytes"`
	RepeatedStart bool   `json:"repeated_start,omitempty"`
	GapTerminated bool   `json:"gap_terminated,omitempty"`
	Stretched     bool   `json:"stretched,omitempty"`
	StartNs       int64  `json:"start_ns"`
	EndNs         int64  `json:"end_ns"`
}

// Deps records exactly which material, parameters and decoder version built a
// candidate, plus the capture segments it depends on.
type Deps struct {
	MaterialDigest string   `json:"material_digest"`
	ParamVersion   int      `json:"param_version"`
	DecoderVersion string   `json:"decoder_version"`
	Segments       []string `json:"segments"`
}

// Candidate is one legal interpretation of the evidence.
type Candidate struct {
	ID            string        `json:"id"`
	Decoder       string        `json:"decoder"`
	Mode          string        `json:"mode,omitempty"`
	Confidence    float64       `json:"confidence"`
	Transactions  []Transaction `json:"transactions"`
	Diagnostics   []Diagnostic  `json:"diagnostics,omitempty"`
	Deps          Deps          `json:"deps"`
	Status        string        `json:"status"`
	Segment       string        `json:"segment"`
}

// Result is the outcome of a decode run.
type Result struct {
	Kind       ResultKind
	Candidates []Candidate
	Diagnostics []Diagnostic
}

type levelEdge struct {
	id     string
	idx    int // index into the capture's sorted edge list
	t      int64
	level  int
}

type prepared struct {
	byChannel map[string][]levelEdge
	alias     map[string]string
	channels  []model.Channel
	diag      []Diagnostic
	valid     []model.Edge // sorted valid edges (global index basis)
}

// prepare sorts edges deterministically (equivalent input -> stable order),
// applies level inversion, and reports unconvertible timestamps in place.
func prepare(c *model.Capture) *prepared {
	p := &prepared{
		byChannel: map[string][]levelEdge{},
		alias:     map[string]string{},
		channels:  c.Channels,
	}
	edges := make([]model.Edge, len(c.Edges))
	copy(edges, c.Edges)
	sort.SliceStable(edges, func(i, j int) bool {
		if edges[i].TimeNs != edges[j].TimeNs {
			return edges[i].TimeNs < edges[j].TimeNs
		}
		if edges[i].Channel != edges[j].Channel {
			return edges[i].Channel < edges[j].Channel
		}
		return edges[i].ID < edges[j].ID
	})
	inv := map[string]bool{}
	for _, ch := range c.Channels {
		p.alias[ch.ID] = ch.Alias
		inv[ch.ID] = ch.Invert
	}
	for gi, e := range edges {
		if e.TimeNs < 0 {
			p.diag = append(p.diag, Diagnostic{
				Code: "unconvertible-value",
				Message: "edge timestamp cannot be converted; left in place and excluded from decoding: " + e.Raw,
				Channel: e.Channel, EdgeID: e.ID, TimeNs: -1,
			})
			continue
		}
		lvl := e.Level
		if inv[e.Channel] {
			lvl ^= 1
		}
		p.valid = append(p.valid, e)
		p.byChannel[e.Channel] = append(p.byChannel[e.Channel], levelEdge{id: e.ID, idx: gi, t: e.TimeNs, level: lvl})
	}
	return p
}

// effectiveLevel finds the level of channel ch at time t (last edge <= t).
func (p *prepared) effectiveLevel(ch string, t int64) int {
	lvl := 1
	for _, e := range p.byChannel[ch] {
		if e.t <= t {
			lvl = e.level
		} else {
			break
		}
	}
	return lvl
}

func (p *prepared) channel(role string) (model.Channel, bool) {
	for _, ch := range p.iterChannels() {
		if ch.Role == role {
			return ch, true
		}
	}
	return model.Channel{}, false
}

// iterChannels is filled from the capture via a field set by Run.
func (p *prepared) iterChannels() []model.Channel { return p.channels }

// segmentFor maps a time stamp to its declared segment.
func segmentFor(c *model.Capture, t int64) string {
	for _, s := range c.Segments {
		if t >= s.StartNs && t <= s.EndNs {
			return s.ID
		}
	}
	return "unsegmented"
}

func medianPeriod(es []levelEdge) int64 {
	if len(es) < 3 {
		return 0
	}
	deltas := make([]int64, 0, len(es)-1)
	for i := 1; i < len(es); i++ {
		deltas = append(deltas, es[i].t-es[i-1].t)
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	return deltas[len(deltas)/2]
}

func stableID(prefix string, parts ...any) string {
	return prefix + "-" + model.Digest(parts)
}

// Run executes every applicable decoder and returns a merged, deterministically
// ordered candidate set.
func Run(c *model.Capture, params model.Params, budget Budget) Result {
	if len(c.Edges) > budget.MaxEdges {
		return Result{Kind: BudgetExhausted, Diagnostics: []Diagnostic{{
			Code: "budget-exhausted",
			Message: "edge count exceeds resource budget",
			TimeNs: -1,
		}}}
	}
	hasRole := func(role string) bool {
		for _, ch := range c.Channels {
			if ch.Role == role {
				return true
			}
		}
		return false
	}
	var all []Candidate
	var diag []Diagnostic
	add := func(r Result) {
		if r.Kind != Ok {
			all = nil
			diag = append(diag, r.Diagnostics...)
			return
		}
		all = append(all, r.Candidates...)
		diag = append(diag, r.Diagnostics...)
	}
	if hasRole("scl") && hasRole("sda") {
		add(DecodeI2C(c, params, budget))
	}
	if hasRole("clk") && hasRole("mosi") {
		add(DecodeSPI(c, params, budget))
	}
	if hasRole("syncclk") && hasRole("syncdata") {
		add(DecodeSync(c, params, budget))
	}
	if len(all) > budget.MaxCandidates {
		return Result{Kind: BudgetExhausted, Diagnostics: append(diag, Diagnostic{
			Code: "budget-exhausted", Message: "candidate count exceeds resource budget", TimeNs: -1,
		})}
	}
	sort.SliceStable(all, func(i, j int) bool {
		ai, aj := int64(1<<62), int64(1<<62)
		if n := len(all[i].Transactions); n > 0 {
			ai = all[i].Transactions[0].StartNs
		}
		if n := len(all[j].Transactions); n > 0 {
			aj = all[j].Transactions[0].StartNs
		}
		if ai != aj {
			return ai < aj
		}
		if all[i].Decoder != all[j].Decoder {
			return all[i].Decoder < all[j].Decoder
		}
		if all[i].Mode != all[j].Mode {
			return all[i].Mode < all[j].Mode
		}
		return all[i].ID < all[j].ID
	})
	return Result{Kind: Ok, Candidates: all, Diagnostics: diag}
}

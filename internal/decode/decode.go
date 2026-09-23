// Package decode turns raw digital channel edges into reviewable I2C, SPI
// and custom synchronous transactions. Ambiguity (missing clock edges,
// repeated START, clock stretching, unknown SPI mode, boundary sampling)
// yields multiple structured candidates; data gaps terminate transactions
// and are never stitched.
package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// Version is stamped on every run so results stay attributable to the exact
// decoder that produced them.
const Version = "edgedesk-decode/1.3.0"

// edgeAt is one effective (inversion-applied) edge on a reconstructed line.
type edgeAt struct {
	t     int64
	level int
	idx   int // index into Capture.Edges (provenance)
}

// line is one channel's reconstructed level timeline.
type line struct {
	name  string
	edges []edgeAt // time-ordered
}

// levelAt returns the effective level just before time t. Before the first
// edge the inverse of the first level is assumed (flagged by the caller).
func (l *line) levelAt(t int64) int {
	if len(l.edges) == 0 {
		return -1
	}
	lvl := 1 - l.edges[0].level
	for _, e := range l.edges {
		if e.t >= t {
			break
		}
		lvl = e.level
	}
	return lvl
}

// levelAfter returns the effective level at/just after time t.
func (l *line) levelAfter(t int64) int {
	if len(l.edges) == 0 {
		return -1
	}
	lvl := 1 - l.edges[0].level
	for _, e := range l.edges {
		if e.t > t {
			break
		}
		lvl = e.level
	}
	return lvl
}

func buildLine(cap *model.Capture, alias string) *line {
	l := &line{name: alias}
	for _, i := range cap.EdgesOf(alias) {
		e := cap.Edges[i]
		l.edges = append(l.edges, edgeAt{t: e.TimeNs, level: cap.EffectiveLevel(e.Channel, e.Level), idx: i})
	}
	return l
}

// findChannel resolves a role/alias to a channel, trying alias then role.
func findChannel(cap *model.Capture, aliases ...string) *model.Channel {
	for _, a := range aliases {
		if ch := cap.ChannelByAlias(a); ch != nil {
			return ch
		}
	}
	return nil
}

func channelsByRole(cap *model.Capture, role string) []model.Channel {
	var out []model.Channel
	for _, ch := range cap.Channels {
		if ch.Role == role {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// ambiguity is one undecidable point that forks candidates.
type ambiguity struct {
	reason     string // missing_clock_edge | boundary_sampling | spi_mode_unknown | ...
	timeNs     int64
	channel    string
	primary    string // hypothesis label of the base candidate
	alternate  string // hypothesis label of the forked candidate
	diagnostic model.Diagnostic
}

// checkBudget enforces the resource budget; exceeding it is its own
// terminal result kind, distinct from format and semantic failures.
func checkBudget(cap *model.Capture, p model.Params) *model.Failure {
	if p.BudgetMaxEdges > 0 && len(cap.Edges) > p.BudgetMaxEdges {
		return &model.Failure{
			Kind:    model.FailureBudget,
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(cap.Edges), p.BudgetMaxEdges),
			Diagnostics: []model.Diagnostic{{
				Code: "BUDGET_EDGES", Severity: "error", EdgeIndex: p.BudgetMaxEdges, TimeNs: -1,
				Message: fmt.Sprintf("edge budget exhausted at edge %d", p.BudgetMaxEdges),
			}},
		}
	}
	return nil
}

func capCandidates(cs []model.Candidate, p model.Params) (*model.Failure, []model.Candidate) {
	if p.BudgetMaxCandidates > 0 && len(cs) > p.BudgetMaxCandidates {
		return &model.Failure{
			Kind:    model.FailureBudget,
			Message: fmt.Sprintf("candidate count %d exceeds budget %d", len(cs), p.BudgetMaxCandidates),
		}, nil
	}
	return nil, cs
}

// Decode dispatches per protocol. Protocols: "i2c", "spi", "sync".
func Decode(cap *model.Capture, p model.Params, protocol string) model.DecodeResult {
	if f := checkBudget(cap, p); f != nil {
		return model.DecodeResult{Protocol: protocol, Failure: f}
	}
	var r model.DecodeResult
	switch protocol {
	case "i2c":
		r = decodeI2C(cap, p)
	case "spi":
		r = decodeSPI(cap, p)
	case "sync":
		r = decodeSync(cap, p)
	default:
		return modelFailure(protocol)
	}
	r.Protocol = protocol
	if r.Failure == nil && len(r.Candidates) == 0 {
		r.Failure = &model.Failure{
			Kind:    model.FailureSemantic,
			Message: fmt.Sprintf("no legal %s decode exists for the given evidence", protocol),
		}
	}
	if r.Failure == nil {
		if f, cs := capCandidates(r.Candidates, p); f != nil {
			r.Failure = f
			r.Candidates = nil
		} else {
			model.SortCandidates(cs)
			r.Candidates = cs
		}
	}
	return r
}

func modelFailure(protocol string) model.DecodeResult {
	return model.DecodeResult{Protocol: protocol, Failure: &model.Failure{
		Kind: model.FailureSemantic, Message: "unknown protocol " + protocol,
	}}
}

// ProtocolsPresent lists protocols decodable from the channel config.
func ProtocolsPresent(cap *model.Capture) []string {
	var out []string
	if findChannel(cap, "SDA") != nil && findChannel(cap, "SCL") != nil {
		out = append(out, "i2c")
	}
	if findChannel(cap, "SCLK") != nil && len(channelsByRole(cap, "cs")) > 0 {
		out = append(out, "spi")
	}
	if findChannel(cap, "SYNC_CLK") != nil && findChannel(cap, "SYNC_DATA") != nil {
		out = append(out, "sync")
	}
	return out
}

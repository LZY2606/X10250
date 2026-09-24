// Package decode translates digital channel edges into reviewable
// I2C / SPI / custom-sync transactions. All rules live server-side and are
// directly testable.
package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// Input is the derived, normalized view handed to decoders.
type Input struct {
	Stream model.EdgeStream
	Config model.ProbeConfig
	Params model.Params
	// levels[ch] = edges with inversion applied, sorted by time
	levels map[string][]model.Edge
	init   map[string]int
	ops    int
}

// NewInput builds the derived view. diags collects positioned problems.
func NewInput(stream model.EdgeStream, cfg model.ProbeConfig, p model.Params) (*Input, []model.Diagnostic) {
	in := &Input{
		Stream: stream,
		Config: cfg,
		Params: p,
		levels: map[string][]model.Edge{},
		init:   map[string]int{},
	}
	var diags []model.Diagnostic
	known := map[string]model.Channel{}
	for _, ch := range stream.Channels {
		known[ch.Alias] = ch
		init := ch.Initial
		if ch.Inverted {
			init = 1 - init
		}
		in.init[ch.Alias] = init
	}
	for _, e := range stream.Edges {
		if e.Invalid {
			diags = append(diags, model.Diagnostic{Code: "unconvertible_edge", Severity: "warning",
				Position: "edges." + e.ID, Message: "edge kept in place but excluded from decode: raw time " + e.TRaw})
			continue
		}
		ch, ok := known[e.Ch]
		if !ok {
			diags = append(diags, model.Diagnostic{Code: "unknown_channel", Severity: "error",
				Position: "edges." + e.ID, Message: "edge references unknown channel alias " + e.Ch})
			continue
		}
		lv := e.Level
		if ch.Inverted {
			lv = 1 - lv
		}
		ne := e
		ne.Level = lv
		in.levels[e.Ch] = append(in.levels[e.Ch], ne)
	}
	for role, alias := range cfg.Roles {
		if _, ok := known[alias]; !ok {
			diags = append(diags, model.Diagnostic{Code: "role_unassigned", Severity: "error",
				Position: "roles." + role, Message: "role " + role + " maps to missing channel " + alias})
		}
	}
	return in, diags
}

func (in *Input) edges(role string) []model.Edge {
	alias, ok := in.Config.Roles[role]
	if !ok {
		return nil
	}
	return in.levels[alias]
}

func (in *Input) initial(role string) int {
	alias, ok := in.Config.Roles[role]
	if !ok {
		return 0
	}
	return in.init[alias]
}

// levelAt returns the level of a channel at time t (state after the last
// edge at or before t).
func levelAt(edges []model.Edge, initial int, t int64) int {
	lv := initial
	for _, e := range edges {
		if e.TNs > t {
			break
		}
		lv = e.Level
	}
	return lv
}

func (in *Input) spend(n int) bool {
	in.ops += n
	return in.ops <= in.Params.MaxOps
}

// merged returns all edges of the given roles merged and time-sorted.
func (in *Input) merged(roles ...string) []model.Edge {
	var out []model.Edge
	for _, r := range roles {
		out = append(out, in.edges(r)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TNs < out[j].TNs })
	return out
}

// Run executes all decoders whose roles are assigned and merges candidates.
// Status precedence: corrupt_input > budget_exhausted > unsolvable > ok.
func Run(stream model.EdgeStream, cfg model.ProbeConfig, p model.Params) model.RunResult {
	in, diags := NewInput(stream, cfg, p)
	res := model.RunResult{Status: model.StatusOK, Diagnostics: diags}
	for _, d := range diags {
		if d.Severity == "error" {
			res.Status = model.StatusCorruptInput
		}
	}
	var cands []model.Candidate
	if res.Status == model.StatusOK {
		if _, ok := cfg.Roles["scl"]; ok {
			sc, sd, st := DecodeI2C(in)
			cands = append(cands, sc...)
			res.Diagnostics = append(res.Diagnostics, sd...)
			res.Status = mergeStatus(res.Status, st)
		}
		if _, ok := cfg.Roles["sck"]; ok {
			sc, sd, st := DecodeSPI(in)
			cands = append(cands, sc...)
			res.Diagnostics = append(res.Diagnostics, sd...)
			res.Status = mergeStatus(res.Status, st)
		}
		if _, ok := cfg.Roles["clk"]; ok {
			sc, sd, st := DecodeSync(in)
			cands = append(cands, sc...)
			res.Diagnostics = append(res.Diagnostics, sd...)
			res.Status = mergeStatus(res.Status, st)
		}
	}
	// stable candidate ordering: confidence desc, then canonical content
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		return cands[i].ID < cands[j].ID
	})
	for i := range cands {
		cands[i].Rank = i
	}
	res.Candidates = cands
	return res
}

func mergeStatus(a, b string) string {
	rank := map[string]int{model.StatusOK: 0, model.StatusUnsolvable: 1,
		model.StatusBudgetExhausted: 2, model.StatusCorruptInput: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// candidateID derives a stable ID from decoder + assumptions + byte values.
func candidateID(decoder string, assumptions []string, txns []model.Transaction) string {
	var parts []any
	parts = append(parts, decoder, assumptions)
	for _, t := range txns {
		var bs []int
		for _, b := range t.Bytes {
			bs = append(bs, b.Value)
		}
		parts = append(parts, t.Bus, t.CS, bs)
	}
	return model.HashID("cand", parts)
}

// assignIDs rewrites transaction/byte IDs to be rooted at the candidate ID.
func assignIDs(c *model.Candidate) {
	for ti := range c.Transactions {
		t := &c.Transactions[ti]
		t.ID = fmt.Sprintf("%s/t%d", c.ID, ti)
		for bi := range t.Bytes {
			t.Bytes[bi].ID = fmt.Sprintf("%s/b%d", t.ID, bi)
		}
	}
}

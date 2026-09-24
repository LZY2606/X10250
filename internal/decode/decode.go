// Package decode translates immutable edge streams into protocol candidates.
// Decoders never mutate their input; ambiguity produces multiple candidates
// instead of a forced answer, and data gaps are never spliced into a
// transaction.
package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

const DecoderVersion = "edgebench/1.0.0"

// Budget caps computation. Exceeding it is a first-class outcome
// (RunBudgetExhausted), distinct from corrupt input and unsolvable input.
type Budget struct {
	MaxEdges      int
	MaxCandidates int
}

func DefaultBudget() Budget { return Budget{MaxEdges: 200000, MaxCandidates: 64} }

// Prepared is the derived, normalized view of a capture. The raw capture is
// untouched; inversion is applied here only.
type Prepared struct {
	Cap       *model.Capture
	ByRole    map[string]string   // role -> channel name (first wins)
	ByRoleAll map[string][]string // role -> all channel names (multi CS)
	Levels    map[string][]int    // channel -> normalized levels, aligned with Edges
	Times     map[string][]int64
	EdgeRefs  map[string][]int // channel -> indices into Cap.Edges
}

// Prepare validates format and builds the normalized view. Format problems
// (unknown channels, non-monotonic time, non-alternating levels) are
// reported with positions; nothing is silently repaired.
func Prepare(cap *model.Capture) (*Prepared, []model.Diagnostic) {
	p := &Prepared{
		Cap:       cap,
		ByRole:    map[string]string{},
		ByRoleAll: map[string][]string{},
		Levels:    map[string][]int{},
		Times:     map[string][]int64{},
		EdgeRefs:  map[string][]int{},
	}
	known := map[string]model.ChannelDef{}
	for _, ch := range cap.Channels {
		known[ch.Name] = ch
		if ch.Role != "other" {
			if _, ok := p.ByRole[ch.Role]; !ok {
				p.ByRole[ch.Role] = ch.Name
			}
			p.ByRoleAll[ch.Role] = append(p.ByRoleAll[ch.Role], ch.Name)
		}
	}
	var diags []model.Diagnostic
	lastT := map[string]int64{}
	lastLvl := map[string]int{}
	started := map[string]bool{}
	for i, e := range cap.Edges {
		def, ok := known[e.Ch]
		if !ok {
			diags = append(diags, model.Diagnostic{Code: model.CodeFormatCorrupt, Severity: "error",
				Message: fmt.Sprintf("edge references unknown channel %q", e.Ch), Channel: e.Ch, EdgeIndex: i, TimeNs: e.T})
			continue
		}
		if e.Level != 0 && e.Level != 1 {
			diags = append(diags, model.Diagnostic{Code: model.CodeFormatCorrupt, Severity: "error",
				Message: "edge level must be 0 or 1", Channel: e.Ch, EdgeIndex: i, TimeNs: e.T})
			continue
		}
		if started[e.Ch] && e.T < lastT[e.Ch] {
			diags = append(diags, model.Diagnostic{Code: model.CodeFormatCorrupt, Severity: "error",
				Message: "edge time goes backwards", Channel: e.Ch, EdgeIndex: i, TimeNs: e.T})
			continue
		}
		if started[e.Ch] && e.Level == lastLvl[e.Ch] {
			diags = append(diags, model.Diagnostic{Code: model.CodeFormatCorrupt, Severity: "error",
				Message: "consecutive edges with identical level (not a transition)", Channel: e.Ch, EdgeIndex: i, TimeNs: e.T})
			continue
		}
		lvl := e.Level
		if def.Inverted {
			lvl = 1 - lvl
		}
		p.Levels[e.Ch] = append(p.Levels[e.Ch], lvl)
		p.Times[e.Ch] = append(p.Times[e.Ch], e.T)
		p.EdgeRefs[e.Ch] = append(p.EdgeRefs[e.Ch], i)
		lastT[e.Ch] = e.T
		lastLvl[e.Ch] = e.Level
		started[e.Ch] = true
	}
	// Bounce detection: same-channel edges closer than one sample period are
	// flagged (warn) but kept; reviewers decide via source revision.
	if cap.SampleRateHz > 0 {
		minNs := int64(1e9 / cap.SampleRateHz)
		for ch, ts := range p.Times {
			for i := 1; i < len(ts); i++ {
				if ts[i]-ts[i-1] < minNs {
					diags = append(diags, model.Diagnostic{Code: model.CodeEdgeBounce, Severity: "warn",
						Message: fmt.Sprintf("edges %.1f ns apart, below sample period", float64(ts[i]-ts[i-1])),
						Channel: ch, EdgeIndex: p.EdgeRefs[ch][i], TimeNs: ts[i]})
				}
			}
		}
	}
	sort.SliceStable(diags, func(i, j int) bool { return diags[i].TimeNs < diags[j].TimeNs })
	return p, diags
}

// levelAt returns the normalized level of ch at time t (level after the last
// edge at or before t). defaultLvl is used before the first edge.
func (p *Prepared) levelAt(ch string, t int64, defaultLvl int) int {
	ts := p.Times[ch]
	i := sort.Search(len(ts), func(i int) bool { return ts[i] > t }) - 1
	if i < 0 {
		return defaultLvl
	}
	return p.Levels[ch][i]
}

// Result is the full output of a decode run.
type Result struct {
	Status      string
	Diagnostics []model.Diagnostic
	Candidates  []*model.Candidate
}

// Run decodes a capture under a config. It is deterministic: equivalent
// inputs yield identical candidates in identical order.
func Run(cap *model.Capture, cfg *model.ProbeConfig, b Budget) Result {
	prep, diags := Prepare(cap)
	for _, d := range diags {
		if d.Severity == "error" {
			return Result{Status: model.RunFormatCorrupt, Diagnostics: diags}
		}
	}
	if len(cap.Edges) > b.MaxEdges {
		return Result{Status: model.RunBudgetExhausted, Diagnostics: append(diags, model.Diagnostic{
			Code: model.CodeBudgetExhausted, Severity: "error",
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(cap.Edges), b.MaxEdges)})}
	}
	var cands []*model.Candidate
	overflow := false
	add := func(cs []*model.Candidate, ds []model.Diagnostic, fatal *model.Diagnostic) {
		diags = append(diags, ds...)
		if fatal != nil {
			diags = append(diags, *fatal)
			return
		}
		cands = append(cands, cs...)
		if len(cands) > b.MaxCandidates {
			overflow = true
		}
	}
	c, ds, f := DecodeI2C(prep, cfg)
	add(c, ds, f)
	c, ds, f = DecodeSPI(prep, cfg)
	add(c, ds, f)
	c, ds, f = DecodeSync(prep, cfg)
	add(c, ds, f)
	if overflow {
		return Result{Status: model.RunBudgetExhausted, Diagnostics: append(diags, model.Diagnostic{
			Code: model.CodeBudgetExhausted, Severity: "error",
			Message: fmt.Sprintf("candidate count exceeds budget %d", b.MaxCandidates)})}
	}
	if len(cands) == 0 {
		return Result{Status: model.RunUnsolvable, Diagnostics: append(diags, model.Diagnostic{
			Code: model.CodeUnsolvable, Severity: "error",
			Message: "no legal decode candidate exists for the given channels and parameters"})}
	}
	return Result{Status: model.RunOK, Diagnostics: diags, Candidates: cands}
}

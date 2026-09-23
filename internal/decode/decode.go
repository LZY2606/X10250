// Package decode turns raw digital edges into I2C / SPI / custom-sync
// transactions. Ambiguity (missing clock edges, boundary samples, unknown
// SPI mode) forks multiple candidates instead of silently picking one;
// data gaps terminate transactions and are never stitched.
package decode

import (
	"fmt"
	"sort"

	"edgeworkbench/internal/model"
)

// evtDraft is a decode-time judgement before persistence assigns a Seq.
type evtDraft struct {
	Kind      string
	Channel   string
	TimeNs    int64
	Rationale string
}

// busResult is the per-bus decode output.
type busResult struct {
	candidates []model.Candidate
	events     []evtDraft
	diags      []model.Diagnostic
}

// Output is the full derived result for one (capture, params) pair.
type Output struct {
	Status      string
	Candidates  []model.Candidate
	Events      []evtDraft
	Diagnostics []model.Diagnostic
}

func levelOf(ch model.Channel, raw int) int {
	if ch.Inverted {
		if raw == 0 {
			return 1
		}
		return 0
	}
	return raw
}

func chanByRole(cap_ *model.Capture, role string) (model.Channel, bool) {
	for _, c := range cap_.Channels {
		if c.Role == role {
			return c, true
		}
	}
	return model.Channel{}, false
}

func chansByRole(cap_ *model.Capture, role string) []model.Channel {
	var out []model.Channel
	for _, c := range cap_.Channels {
		if c.Role == role {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out
}

// Run decodes a capture under one params version. It is pure: same inputs
// always yield the same candidates in the same order.
func Run(c *model.Capture, p *model.Params) Output {
	out := Output{Status: model.StatusOK}
	for _, d := range c.Diagnostics {
		if d.Category == model.DiagFormatCorrupt {
			out.Status = model.StatusFormatCorrupt
			out.Diagnostics = append(out.Diagnostics, d)
			return out
		}
	}
	if p.BudgetMaxEdges > 0 && len(c.Edges) > p.BudgetMaxEdges {
		out.Status = model.StatusBudgetExhausted
		out.Diagnostics = append(out.Diagnostics, model.Diagnostic{
			Category: model.DiagBudget,
			Position: fmt.Sprintf("capture:%s", c.ID),
			Message:  fmt.Sprintf("edge count %d exceeds budget %d", len(c.Edges), p.BudgetMaxEdges),
		})
		return out
	}
	if len(c.Edges) == 0 {
		out.Status = model.StatusSemanticUnsolvable
		out.Diagnostics = append(out.Diagnostics, model.Diagnostic{
			Category: model.DiagSemantic,
			Position: fmt.Sprintf("capture:%s", c.ID),
			Message:  "capture contains no edges; nothing can be decoded",
		})
		return out
	}

	budget := &candidateBudget{max: p.BudgetMaxCandidates}
	var buses []busResult
	if r := decodeI2C(c, p, budget); r != nil {
		buses = append(buses, *r)
	}
	if budget.exceeded() {
		return budgetOut(c, p)
	}
	if r := decodeSPI(c, p, budget); r != nil {
		buses = append(buses, *r)
	}
	if budget.exceeded() {
		return budgetOut(c, p)
	}
	if r := decodeSync(c, p, budget); r != nil {
		buses = append(buses, *r)
	}
	if budget.exceeded() {
		return budgetOut(c, p)
	}

	// Stable order: by bus, then reason, then first transaction start.
	for _, b := range buses {
		out.Candidates = append(out.Candidates, b.candidates...)
		out.Events = append(out.Events, b.events...)
		out.Diagnostics = append(out.Diagnostics, b.diags...)
	}
	sort.SliceStable(out.Candidates, func(i, j int) bool {
		a, b := out.Candidates[i], out.Candidates[j]
		if a.Bus != b.Bus {
			return a.Bus < b.Bus
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.ID < b.ID
	})
	if len(out.Candidates) == 0 {
		out.Status = model.StatusSemanticUnsolvable
		out.Diagnostics = append(out.Diagnostics, model.Diagnostic{
			Category: model.DiagSemantic,
			Position: fmt.Sprintf("capture:%s", c.ID),
			Message:  "no decodable bus activity found on any probed channel",
		})
	}
	return out
}

func budgetOut(c *model.Capture, p *model.Params) Output {
	return Output{
		Status: model.StatusBudgetExhausted,
		Diagnostics: []model.Diagnostic{{
			Category: model.DiagBudget,
			Position: fmt.Sprintf("capture:%s", c.ID),
			Message:  fmt.Sprintf("candidate count exceeds budget %d", p.BudgetMaxCandidates),
		}},
	}
}

type candidateBudget struct {
	max   int
	count int
}

func (b *candidateBudget) fork(n int) {
	b.count += n
}
func (b *candidateBudget) exceeded() bool {
	return b.max > 0 && b.count > b.max
}

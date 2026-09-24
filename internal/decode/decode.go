// Package decode translates normalized digital edges into reviewable
// I2C, SPI and custom-synchronous transactions. All rules run server-side.
package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// Run executes every applicable protocol decoder over one import and
// assembles a DecodeResult with stable candidate ordering.
func Run(imp *model.Import, p *model.Params) *model.DecodeResult {
	res := &model.DecodeResult{
		ImportID:  imp.ID,
		ParamsID:  p.ID,
		ParamsVer: p.Version,
	}
	res.InputDigest = model.HashID("dig", imp.Digest, p.ID)
	res.ID = model.HashID("dec", res.InputDigest)

	if len(imp.Edges) > p.MaxEdges {
		res.Status = model.StatusResourceExhausted
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Severity: "error", Path: "edges",
			Message: fmt.Sprintf("edge count %d exceeds budget max_edges=%d", len(imp.Edges), p.MaxEdges)})
		return res
	}

	norm, diags := imp.Normalized()
	corrupt := false
	for _, d := range diags {
		if d.Severity == "error" {
			corrupt = true
		}
	}
	res.Diagnostics = diags
	if corrupt {
		res.Status = model.StatusCorrupt
		return res
	}

	roles := map[string][]string{}
	for _, c := range imp.Config.Channels {
		roles[c.Role] = append(roles[c.Role], c.Name)
	}
	pick := func(role string) []string { return roles[role] }

	matched := false
	var cands []model.Candidate
	var judgments []model.Judgment

	if len(pick("scl")) > 0 && len(pick("sda")) > 0 {
		matched = true
		cs, js := decodeI2C(norm[pick("scl")[0]], norm[pick("sda")[0]], p,
			pick("scl")[0], pick("sda")[0], res.ID)
		cands = append(cands, cs...)
		judgments = append(judgments, js...)
	}
	if len(pick("sclk")) > 0 && len(pick("cs")) > 0 && (len(pick("mosi")) > 0 || len(pick("miso")) > 0) {
		matched = true
		css := map[string][]model.LEdge{}
		for _, ch := range pick("cs") {
			css[ch] = norm[ch]
		}
		var mosi, miso []model.LEdge
		mosiCh, misoCh := "", ""
		if len(pick("mosi")) > 0 {
			mosi = norm[pick("mosi")[0]]
			mosiCh = pick("mosi")[0]
		}
		if len(pick("miso")) > 0 {
			miso = norm[pick("miso")[0]]
			misoCh = pick("miso")[0]
		}
		cs, js := decodeSPI(norm[pick("sclk")[0]], mosi, miso, mosiCh, misoCh, css, p, res.ID)
		cands = append(cands, cs...)
		judgments = append(judgments, js...)
	}
	if len(pick("clock")) > 0 && len(pick("data")) > 0 {
		matched = true
		cs, js := decodeSync(norm[pick("clock")[0]], norm[pick("data")[0]], p,
			pick("clock")[0], pick("data")[0], res.ID)
		cands = append(cands, cs...)
		judgments = append(judgments, js...)
	}

	if !matched {
		res.Status = model.StatusCorrupt
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Severity: "error", Path: "config.channels",
			Message:  "no protocol role set matched (need scl+sda, sclk+cs+mosi/miso, or clock+data)"})
		return res
	}

	if len(cands) > p.MaxCandidates {
		res.Status = model.StatusResourceExhausted
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Severity: "error", Path: "candidates",
			Message: fmt.Sprintf("candidate count %d exceeds budget max_candidates=%d",
				len(cands), p.MaxCandidates)})
		res.Candidates = nil
		return res
	}

	// Equivalent input yields stable ordering: protocol, confidence, hypothesis.
	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].Protocol != cands[b].Protocol {
			return cands[a].Protocol < cands[b].Protocol
		}
		if cands[a].Confidence != cands[b].Confidence {
			return cands[a].Confidence > cands[b].Confidence
		}
		return cands[a].Hypothesis < cands[b].Hypothesis
	})

	res.Candidates = cands
	res.Judgments = judgments
	switch {
	case len(cands) == 0:
		res.Status = model.StatusUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Severity: "error", Path: "decode",
			Message:  "input parsed but no legal transaction could be decoded"})
	case len(cands) > 1:
		res.Status = model.StatusAmbiguous
	default:
		res.Status = model.StatusOK
	}
	return res
}

// levelAt returns the level of a channel at time t given its initial level.
func levelAt(edges []model.LEdge, t int64, init int) int {
	i := sort.Search(len(edges), func(i int) bool { return edges[i].T > t })
	if i == 0 {
		return init
	}
	return edges[i-1].Lvl
}

// nearestDist returns the distance from t to the nearest edge.
func nearestDist(edges []model.LEdge, t int64) int64 {
	if len(edges) == 0 {
		return 1 << 62
	}
	i := sort.Search(len(edges), func(i int) bool { return edges[i].T >= t })
	best := int64(1 << 62)
	if i < len(edges) {
		best = edges[i].T - t
	}
	if i > 0 && t-edges[i-1].T < best {
		best = t - edges[i-1].T
	}
	return best
}

func initLevel(edges []model.LEdge, idleHigh bool) int {
	if len(edges) == 0 {
		if idleHigh {
			return 1
		}
		return 0
	}
	return 1 - edges[0].Lvl
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

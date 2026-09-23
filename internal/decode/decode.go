package decode

import (
	"fmt"
	"sort"
)

// levelAt returns a function giving the channel level at time t
// (after inversion), or -1 before the first known edge.
func levelAt(edges []NormEdge) func(int64) int {
	return func(t int64) int {
		lvl := -1
		// binary search for last edge with TimePS <= t
		lo, hi := 0, len(edges)
		for lo < hi {
			mid := (lo + hi) / 2
			if edges[mid].TimePS <= t {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo == 0 {
			return -1
		}
		lvl = edges[lo-1].Level
		return lvl
	}
}

func byChannel(edges []NormEdge) map[string][]NormEdge {
	out := map[string][]NormEdge{}
	for _, e := range edges {
		out[e.Channel] = append(out[e.Channel], e)
	}
	return out
}

func findChan(m *Material, bus string, roles ...string) string {
	for _, c := range m.Channels {
		if c.Bus != bus {
			continue
		}
		for _, r := range roles {
			if c.Role == r {
				return c.Alias
			}
		}
	}
	return ""
}

func findChans(m *Material, bus, role string) []string {
	var out []string
	for _, c := range m.Channels {
		if c.Bus == bus && c.Role == role {
			out = append(out, c.Alias)
		}
	}
	sort.Strings(out)
	return out
}

func (p *Params) polarityFor(bus string, cfg ChannelConfig) string {
	if p != nil && p.ClockPolarity != nil {
		if pol, ok := p.ClockPolarity[bus]; ok && pol != "" {
			return pol
		}
	}
	if cfg.ClockPolarity != "" {
		return cfg.ClockPolarity
	}
	return "rising"
}

// Decode runs every bus decoder present in the material and merges the
// outcome. Status precedence: corrupt_format > budget_exhausted >
// unsolvable > ok.
func Decode(m *Material, p *Params) *Result {
	if p == nil {
		def := DefaultParams()
		p = &def
	}
	res := &Result{DecoderVersion: m.DecoderVersion}
	edges, diags, corrupt := Normalize(m)
	res.Diagnostics = append(res.Diagnostics, diags...)
	res.InputDigest = Digest(edges, p, m.DecoderVersion)
	if corrupt {
		res.Status = StatusCorruptFormat
		res.Events = append(res.Events, Event{Kind: EvDecodeStatus, Code: string(StatusCorruptFormat)})
		return res
	}
	if p.MaxEdges > 0 && len(edges) > p.MaxEdges {
		res.Status = StatusBudgetExhausted
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: "error", Code: "budget_edges",
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(edges), p.MaxEdges)})
		res.Events = append(res.Events, Event{Kind: EvDecodeStatus, Code: string(StatusBudgetExhausted)})
		return res
	}

	var all []Candidate
	decodedAny := false
	buses := map[string]bool{}
	for _, c := range m.Channels {
		buses[c.Bus] = true
	}
	for _, bus := range []string{"i2c", "spi", "sync"} {
		if !buses[bus] {
			continue
		}
		var cands []Candidate
		var d2 []Diagnostic
		var ev2 []Event
		var okBus bool
		switch bus {
		case "i2c":
			cands, d2, ev2, okBus = decodeI2C(m, edges, p)
		case "spi":
			cands, d2, ev2, okBus = decodeSPI(m, edges, p)
		case "sync":
			cands, d2, ev2, okBus = decodeSync(m, edges, p)
		}
		res.Diagnostics = append(res.Diagnostics, d2...)
		res.Events = append(res.Events, ev2...)
		all = append(all, cands...)
		decodedAny = decodedAny || okBus
	}

	if p.MaxCandidates > 0 && len(all) > p.MaxCandidates {
		res.Status = StatusBudgetExhausted
		res.Diagnostics = append(res.Diagnostics, Diagnostic{Severity: "error", Code: "budget_candidates",
			Message: fmt.Sprintf("candidate count %d exceeds budget %d", len(all), p.MaxCandidates)})
		res.Events = append(res.Events, Event{Kind: EvDecodeStatus, Code: string(StatusBudgetExhausted),
			Detail: map[string]any{"candidates": len(all), "budget": p.MaxCandidates}})
		res.Candidates = nil
		return res
	}

	// Assign stable byte identities derived from candidate + transaction
	// + position, so every displayed value can be traced back to edges.
	for ci := range all {
		for ti := range all[ci].Transactions {
			tx := &all[ci].Transactions[ti]
			for bi := range tx.Bytes {
				b := &tx.Bytes[bi]
				b.ID = shortID("b", fmt.Sprintf("%s|%s|%d|%d", all[ci].ID, tx.ID, bi, b.Value))
			}
		}
	}

	// Stable candidate ordering: confidence desc, then ID asc.
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].Confidence != all[b].Confidence {
			return all[a].Confidence > all[b].Confidence
		}
		return all[a].ID < all[b].ID
	})
	res.Candidates = all
	switch {
	case !decodedAny:
		res.Status = StatusUnsolvable
		res.Events = append(res.Events, Event{Kind: EvDecodeStatus, Code: string(StatusUnsolvable),
			Detail: map[string]any{"reason": "no bus produced any decodable transaction"}})
	default:
		res.Status = StatusOK
		res.Events = append(res.Events, Event{Kind: EvDecodeStatus, Code: string(StatusOK),
			Detail: map[string]any{"candidates": len(all)}})
	}
	return res
}

// DefaultParams are the baseline decision-layer parameters.
func DefaultParams() Params {
	return Params{
		ID:                 "params",
		Version:            1,
		ClockPolarity:      map[string]string{},
		SPIMode:            "unknown",
		GapThresholdPS:     40_000_000, // 40 us
		StretchThresholdPS: 8_000_000,  // 8 us
		BoundaryWindowPS:   300_000,    // 300 ns
		MaxCandidates:      64,
		MaxEdges:           100_000,
	}
}

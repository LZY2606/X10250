// Package decode turns immutable captures + a revisioned ParamSet into
// reviewable transactions. Decoders are pure, deterministic and callable from
// tests directly (no browser involved). Ambiguous conditions produce structured
// CandidateGroups instead of silent guesses, and data gaps never stitch
// fragments into one transaction.
package decode

import (
	"sort"

	"edgebenc/internal/domain"
)

// Signal is the derived (normalized) view of one channel. The material edges
// are untouched; inversion from the probe configuration is applied here.
type Signal struct {
	Ch       domain.Channel
	Times    []float64 // time of each known transition, seconds
	Levels   []bool    // level AFTER each transition
	EdgeIdx  []int     // material edge index for each transition
	HasInit  bool      // initial level before first edge known
	Init     bool
	InvalidT []int // material edge indices whose time could not be normalized
}

// BuildSignals normalizes channels into time-ordered signals.
func BuildSignals(cap *domain.Capture) (map[string]*Signal, []domain.Diagnostic) {
	sigs := map[string]*Signal{}
	var diags []domain.Diagnostic
	for _, ch := range cap.Channels {
		sig := &Signal{Ch: ch}
		raw := cap.Edges[ch.ID]
		// determine first known level to seed toggle inference.
		firstKnown := -1
		for i, e := range raw {
			if e.Level == "high" || e.Level == "low" {
				firstKnown = i
				break
			}
		}
		level := false
		if firstKnown >= 0 {
			v := raw[firstKnown].Level == "high"
			if ch.Invert {
				v = !v
			}
			level = v
			// infer earlier levels by toggling backwards through valid edges
			sig.HasInit = firstKnown == 0
		}
		for i, e := range raw {
			if e.T.Invalid {
				sig.InvalidT = append(sig.InvalidT, i)
				diags = append(diags, domain.Diagnostic{
					Severity: "warning", Code: "edge-time-unconvertible",
					Where: edgeWhere(ch.ID, i),
					Message: "edge excluded from normalized timeline; original text retained on capture",
				})
				continue
			}
			var after bool
			if e.Level == "high" || e.Level == "low" {
				after = e.Level == "high"
				if ch.Invert {
					after = !after
				}
			} else {
				after = !level // toggle from previous inferred level
				if i == 0 {
					diags = append(diags, domain.Diagnostic{Severity: "warning",
						Code: "unknown-boundary-level", Where: edgeWhere(ch.ID, i),
						Message: "first edge level unknown; assumed toggle with low idle"})
				}
			}
			level = after
			sig.Times = append(sig.Times, e.T.Normalized)
			sig.Levels = append(sig.Levels, after)
			sig.EdgeIdx = append(sig.EdgeIdx, i)
		}
		if len(sig.Times) > 0 {
			sig.Init = !sig.Levels[0]
		}
		sigs[ch.Role] = sig
	}
	return sigs, diags
}

func edgeWhere(ch string, i int) string { return "/edges/" + ch + "/" + itoa(i) }

// LevelAt returns the logical level at time t and the material edge index whose
// resulting level applies (-1 if t precedes every edge).
func (s *Signal) LevelAt(t float64) (bool, int) {
	i := sort.Search(len(s.Times), func(i int) bool { return s.Times[i] > t }) - 1
	if i < 0 {
		if s.HasInit {
			return s.Init, -1
		}
		return false, -1
	}
	return s.Levels[i], s.EdgeIdx[i]
}

// EdgeIndexAt returns material edge index of the transition active at t.
func (s *Signal) EdgeIndexAt(t float64) int {
	i := sort.Search(len(s.Times), func(i int) bool { return s.Times[i] > t }) - 1
	if i < 0 {
		return -1
	}
	return s.EdgeIdx[i]
}

// NearestEdge returns the transition closest to t and its time distance.
func (s *Signal) NearestEdge(t float64) (int, float64) {
	idx := sort.Search(len(s.Times), func(i int) bool { return s.Times[i] >= t })
	best, bestD := -1, 0.0
	for j := idx - 1; j <= idx; j++ {
		if j < 0 || j >= len(s.Times) {
			continue
		}
		d := t - s.Times[j]
		if d < 0 {
			d = -d
		}
		if best == -1 || d < bestD {
			best, bestD = j, d
		}
	}
	if best == -1 {
		return -1, 0
	}
	return s.EdgeIdx[best], bestD
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

// mergeTimes returns the sorted union of transition times of the given signals.
func mergeTimes(sigs ...*Signal) []float64 {
	set := map[float64]struct{}{}
	for _, s := range sigs {
		for _, t := range s.Times {
			set[t] = struct{}{}
		}
	}
	out := make([]float64, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Float64s(out)
	return out
}

// roleByID helps build EdgeRef values.
func roleRef(sig *Signal, edgeIdx int) domain.EdgeRef {
	return domain.EdgeRef{Channel: sig.Ch.ID, Index: edgeIdx}
}

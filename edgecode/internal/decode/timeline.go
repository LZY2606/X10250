package decode

import (
	"sort"

	"edgecode/internal/domain"
)

// step is one effective level transition after alias/inversion normalization.
type step struct {
	t     float64
	level int // 0|1|-1 unknown
	edge  *domain.Edge
}

// timeline is the normalized, inversion-corrected view of one channel.
type timeline struct {
	ch    *domain.Channel
	init  int // initial effective level
	steps []step
	// unknown spans retained so the UI can show invalid regions with reasons
	unknown []domain.Span
}

// buildTimeline sorts edges, applies channel inversion (a derived-layer
// normalization; the raw edge level is never modified), and records spans
// where the level is unknown because an imported value did not convert.
func buildTimeline(cap_ *domain.Capture, chRef string) *timeline {
	ch := cap_.ChannelByRef(chRef)
	if ch == nil {
		return nil
	}
	tl := &timeline{ch: ch, init: -1}
	var raw []domain.Edge
	for i := range cap_.Edges {
		if cap_.Edges[i].ChannelRef == chRef {
			raw = append(raw, cap_.Edges[i])
		}
	}
	sort.SliceStable(raw, func(i, j int) bool {
		return raw[i].At.SIV < raw[j].At.SIV
	})
	for i := range raw {
		e := raw[i]
		lvl := e.LevelAt()
		if lvl != -1 && ch.Inverted {
			lvl ^= 1
		}
		tl.steps = append(tl.steps, step{t: e.At.SIV, level: lvl, edge: &e})
	}
	return tl
}

// setInit fills the initial level. It is a derived assumption only when the
// capture did not preserve one; the assumption is reported as a diagnostic.
func (tl *timeline) setInit(v int) { tl.init = v }

// levelAt returns the effective level at t. -1 means unknown.
func (tl *timeline) levelAt(t float64) int {
	if len(tl.steps) == 0 || t < tl.steps[0].t {
		return tl.init
	}
	lo, hi := 0, len(tl.steps)
	for lo < hi {
		mid := (lo + hi) / 2
		if tl.steps[mid].t <= t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return tl.steps[lo-1].level
}

// edgeAt returns the source edge responsible for the level at t.
func (tl *timeline) edgeAt(t float64) *domain.Edge {
	if len(tl.steps) == 0 || t < tl.steps[0].t {
		return nil
	}
	lo, hi := 0, len(tl.steps)
	for lo < hi {
		mid := (lo + hi) / 2
		if tl.steps[mid].t <= t {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return tl.steps[lo-1].edge
}

// edgesBetween returns steps in (from,to].
func (tl *timeline) edgesBetween(from, to float64) []step {
	out := tl.steps[:0:0]
	for _, s := range tl.steps {
		if s.t > from && s.t <= to {
			out = append(out, s)
		}
	}
	return out
}

// crossing finds the next edge after afterT with a strict level change
// (unknown edges are skipped but mark spans invalid).
func (tl *timeline) crossing(afterT float64, from, to int) *step {
	for i := range tl.steps {
		s := &tl.steps[i]
		if s.t <= afterT {
			continue
		}
		if s.level == to {
			prev := tl.levelAt(s.t - 1e-18)
			if prev == from {
				return s
			}
		}
	}
	return nil
}

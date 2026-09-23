package decode

import (
	"sort"

	"edgebench/internal/model"
)

// trans is one normalized transition. level is nil when unknown.
type trans struct {
	at    float64
	seq   int
	level *bool
}

type timeline struct {
	ch    string
	tr    []trans
	known bool // initial level known
	initL bool
}

func inv(b bool) bool { return !b }

// buildTimelines orders edges by sample index (preferred) or time, applies
// channel inversion to known levels and reports edges that cannot be placed.
func buildTimelines(cap model.Capture) (map[string]*timeline, []model.Diagnostic) {
	var diags []model.Diagnostic
	sr := 0.0
	if cap.SampleRate.OK && cap.SampleRate.SI > 0 {
		sr = cap.SampleRate.SI
	}
	type placed struct {
		e model.Edge
		t float64
		ok bool
	}
	var pl []placed
	for _, e := range cap.Edges {
		switch {
		case e.Sample != nil && sr > 0:
			pl = append(pl, placed{e, float64(*e.Sample) / sr, true})
		case e.At.OK:
			pl = append(pl, placed{e, e.At.SI, true})
		default:
			seq := e.Seq
			diags = append(diags, model.Diagnostic{
				Code: "EDGE_WITHOUT_USABLE_TIME", Severity: "error",
				EdgeSeq: &seq, Channel: e.Ch,
				Message: "edge has neither a usable sample index/time nor a known sample rate; left in place and excluded from decode",
			})
		}
	}
	sort.SliceStable(pl, func(i, j int) bool {
		if pl[i].t != pl[j].t {
			return pl[i].t < pl[j].t
		}
		return pl[i].e.Seq < pl[j].e.Seq
	})
	alias := map[string]model.ChAlias{}
	for _, a := range cap.Aliases {
		alias[a.Raw] = a
	}
	out := map[string]*timeline{}
	for _, p := range pl {
		raw := p.e.Ch
		a, hasAlias := alias[raw]
		key := raw
		if hasAlias && a.Role != "" {
			key = a.Role
		}
		tl := out[key]
		if tl == nil {
			tl = &timeline{ch: key}
			out[key] = tl
		}
		lvl := p.e.Level
		if lvl != nil && hasAlias && a.Invert {
			l := !*lvl
			lvl = &l
		}
		tl.tr = append(tl.tr, trans{at: p.t, seq: p.e.Seq, level: lvl})
	}
	for _, tl := range out {
		if len(tl.tr) > 0 && tl.tr[0].level != nil {
			tl.known = true
			tl.initL = *tl.tr[0].level
		}
	}
	return out, diags
}

// levelAt returns the line level at time t. ok=false when unknown.
func (tl *timeline) levelAt(t float64) (bool, bool) {
	cur := tl.initL
	have := tl.known
	for _, x := range tl.tr {
		if x.at > t {
			break
		}
		if x.level != nil {
			cur = *x.level
			have = true
		} else if have {
			cur = !cur
		} else {
			return false, false
		}
	}
	return cur, have
}

// nearestTrans returns the closest transition to t within tol and its index.
func (tl *timeline) nearestTrans(t, tol float64) (trans, int, bool) {
	i := sort.Search(len(tl.tr), func(i int) bool { return tl.tr[i].at >= t })
	best := -1
	bestD := tol
	for j := i - 1; j <= i; j++ {
		if j < 0 || j >= len(tl.tr) {
			continue
		}
		d := abs(tl.tr[j].at - t)
		if d <= bestD {
			best, bestD = j, d
		}
	}
	if best < 0 {
		return trans{}, -1, false
	}
	return tl.tr[best], best, true
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// transitionsIn returns transitions with at in [lo, hi].
func (tl *timeline) transitionsIn(lo, hi float64) []trans {
	var out []trans
	for _, x := range tl.tr {
		if x.at >= lo && x.at <= hi {
			out = append(out, x)
		}
	}
	return out
}

package decode

import (
	"math/big"
	"sort"
	"strings"

	"edgebench/internal/domain"
)

// chTimeline gives O(log n) level-at-time access over the edges of one
// channel. Levels are post-inversion physical logic levels as imported.
type chTimeline struct {
	ch     string
	edges  []domain.Edge
	idle   int
}

func buildTimeline(cap *domain.Capture, roles map[string]string) map[string]*chTimeline {
	byCh := map[string][]domain.Edge{}
	for _, e := range cap.Edges {
		byCh[e.Ch] = append(byCh[e.Ch], e)
	}
	tl := map[string]*chTimeline{}
	idle := map[string]int{"scl": 1, "sda": 1, "sclk": 0, "mosi": 0, "miso": 0, "sync": 0, "data": 0}
	for role, ch := range roles {
		es := byCh[ch]
		sort.Slice(es, func(i, j int) bool { return es[i].T.Cmp(es[j].T) < 0 })
		l := idle[role]
		if len(es) > 0 {
			l = es[0].Level ^ 1
		}
		tl[role] = &chTimeline{ch: ch, edges: es, idle: l}
	}
	return tl
}

// levelAt returns the level just before/at t and the index of the last edge
// with time <= t (-1 when t precedes the first edge).
func (tl *chTimeline) levelAt(at *big.Rat) (int, int) {
	idx := -1
	lo, hi := 0, len(tl.edges)
	for lo < hi {
		mid := (lo + hi) / 2
		if tl.edges[mid].T.Cmp(at) <= 0 {
			idx = mid
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if idx < 0 {
		return tl.idle, -1
	}
	return tl.edges[idx].Level, idx
}

func (tl *chTimeline) edgeAfter(idx int) (domain.Edge, bool) {
	if idx+1 < len(tl.edges) {
		return tl.edges[idx+1], true
	}
	return domain.Edge{}, false
}

func rat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return new(big.Rat)
	}
	return r
}

func ratInt(i int64) *big.Rat { return new(big.Rat).SetInt64(i) }

// nearZero reports |a-b| <= window.
func nearZero(a, b, win *big.Rat) bool {
	d := new(big.Rat).Sub(a, b)
	if d.Sign() < 0 {
		d.Neg(d)
	}
	return d.Cmp(win) <= 0
}

func minRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) <= 0 {
		return a
	}
	return b
}

func maxRat(a, b *big.Rat) *big.Rat {
	if a.Cmp(b) >= 0 {
		return a
	}
	return b
}

// mergedEdges returns all edges of the given roles in global time order; equal
// timestamps sort by role then channel for deterministic replay.
type gedge struct {
	t    *big.Rat
	role string
	e    domain.Edge
}

func mergedEdges(tl map[string]*chTimeline, roles []string) []gedge {
	var out []gedge
	for _, role := range roles {
		t, ok := tl[role]
		if !ok {
			continue
		}
		for _, e := range t.edges {
			if e.TOk {
				out = append(out, gedge{t: new(big.Rat).Set(e.T), role: role, e: e})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if c := out[i].t.Cmp(out[j].t); c != 0 {
			return c < 0
		}
		if out[i].role != out[j].role {
			return out[i].role < out[j].role
		}
		return out[i].e.Index < out[j].e.Index
	})
	return out
}

func busName(cap *domain.Capture, kind string) string {
	for _, b := range cap.BusDefs {
		if b.Kind == kind {
			return b.Name
		}
	}
	return strings.ToUpper(kind)
}

func rolesFor(cap *domain.Capture, kind, name string) map[string]string {
	for _, b := range cap.BusDefs {
		if b.Kind == kind && (name == "" || b.Name == name) {
			return b.Roles
		}
	}
	if name == "" {
		for _, b := range cap.BusDefs {
			if b.Kind == kind {
				return b.Roles
			}
		}
	}
	return nil
}

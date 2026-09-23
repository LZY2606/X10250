// Package decode turns immutable capture edges plus versioned decoder
// parameters into re-derivable bus candidates. All decisions are explicit:
// ambiguous clock polarity / SPI mode yields multiple legal candidates,
// missing clock edges are structured state, and data gaps are never stitched
// into transactions.
package decode

import (
	"sort"

	"edgetrace/internal/model"
)

// RuleVersion is bumped whenever decoding semantics change; it is recorded on
// every derivation so history can replay against the exact rule set.
const RuleVersion = "edge-decode-1.0.0"

// Sig is one channel's ordered edge view with level-at-time queries.
type Sig struct {
	Channel string
	Alias   string
	edges   []model.Edge // sorted by sample then offset
}

// BuildSigs groups capture edges per channel (sorted deterministically).
func BuildSigs(c *model.Capture) map[string]*Sig {
	out := map[string]*Sig{}
	for _, e := range c.Edges {
		s := out[e.Channel]
		if s == nil {
			s = &Sig{Channel: e.Channel, Alias: e.LogicalCh}
			out[e.Channel] = s
		}
		s.edges = append(s.edges, e)
	}
	for _, s := range out {
		sort.SliceStable(s.edges, func(i, j int) bool {
			a, b := s.edges[i], s.edges[j]
			if a.Sample != b.Sample {
				return a.Sample < b.Sample
			}
			if a.OffsetNs != nil && b.OffsetNs != nil {
				if cmp := a.OffsetNs.Cmp(*b.OffsetNs); cmp != 0 {
					return cmp < 0
				}
			}
			return a.ID < b.ID
		})
	}
	return out
}

// Edges returns the ordered edges.
func (s *Sig) Edges() []model.Edge { return s.edges }

// LevelAt returns the logical level in effect at sample index n and the
// edge that most recently drove it ("" before the first edge).
func (s *Sig) LevelAt(n int64) (int, string) {
	level := -1
	id := ""
	for i := range s.edges {
		if s.edges[i].Sample <= n {
			level = s.edges[i].Level
			id = s.edges[i].ID
		} else {
			break
		}
	}
	return level, id
}

// LevelAtNs is LevelAt using exact offsets.
func (s *Sig) LevelAtNs(t model.Rat) (int, string) {
	level := -1
	id := ""
	for i := range s.edges {
		e := s.edges[i]
		if e.OffsetNs == nil || e.OffsetNs.Cmp(t) <= 0 {
			level = e.Level
			id = e.ID
		} else {
			break
		}
	}
	return level, id
}

// CrossingAt reports whether edges on s occur exactly at sample n, returning
// their new levels in edge-ID order.
func (s *Sig) CrossingAt(n int64) []model.Edge {
	var out []model.Edge
	for i := range s.edges {
		if s.edges[i].Sample == n {
			out = append(out, s.edges[i])
		}
		if s.edges[i].Sample > n {
			break
		}
	}
	return out
}

// FirstEdge / LastEdge bound the valid display interval.
func (s *Sig) FirstEdge() (model.Edge, bool) {
	if len(s.edges) == 0 {
		return model.Edge{}, false
	}
	return s.edges[0], true
}

func (s *Sig) LastEdge() (model.Edge, bool) {
	if len(s.edges) == 0 {
		return model.Edge{}, false
	}
	return s.edges[len(s.edges)-1], true
}

// ctx is the per-bus decode context.
type ctx struct {
	cap   *model.Capture
	sigs  map[string]*Sig
	par   model.Params
	bus   model.Bus
	diags []model.Diagnostic
}

func (x *ctx) sig(role string) *Sig {
	ch, ok := x.bus.Roles[role]
	if !ok {
		return nil
	}
	return x.sigs[ch]
}

func (x *ctx) addDiag(d model.Diagnostic) { x.diags = append(x.diags, d) }

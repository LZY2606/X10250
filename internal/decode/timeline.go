package decode

import (
	"sort"

	"edgebench/internal/model"
)

// lineLevel is one known constant level of a channel on a time interval.
type lineLevel struct {
	fromSeq int
	fromPs  int64
	level   int // physical level
}

// timeline normalizes capture edges into per-channel piecewise-constant levels.
type timeline struct {
	cap      *model.Capture
	byAlias  map[string]*model.ChannelDef
	byChan   map[string][]lineLevel
	edgeBySeq map[int]model.Edge
}

func buildTimeline(c *model.Capture) *timeline {
	tl := &timeline{
		cap: c,
		byAlias: map[string]*model.ChannelDef{},
		byChan:  map[string][]lineLevel{},
		edgeBySeq: map[int]model.Edge{},
	}
	for i := range c.Channels {
		ch := c.Channels[i]
		tl.byAlias[ch.Alias] = &ch
	}
	for _, e := range c.Edges {
		tl.edgeBySeq[e.Seq] = e
	}
	per := map[string][]model.Edge{}
	for _, e := range c.Edges {
		if !e.TimeOK || !e.LevelOK {
			continue
		}
		per[e.Channel] = append(per[e.Channel], e)
	}
	for alias, list := range per {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Picoseconds != list[j].Picoseconds {
				return list[i].Picoseconds < list[j].Picoseconds
			}
			return list[i].Seq < list[j].Seq
		})
		ll := make([]lineLevel, 0, len(list))
		for _, e := range list {
			lv := *e.Level
			if ch := tl.byAlias[alias]; ch != nil && ch.Invert {
				lv ^= 1
			}
			ll = append(ll, lineLevel{fromSeq: e.Seq, fromPs: e.Picoseconds, level: lv})
		}
		tl.byChan[alias] = ll
	}
	return tl
}

func (tl *timeline) hasChannel(alias string) bool {
	_, ok := tl.byAlias[alias]
	return ok
}

// levelAt returns the logical level effective immediately before an edge and
// the sequence of the edge that established it.
func (tl *timeline) levelAt(alias string, atPs int64, edgeSeq int) (level int, fromSeq int, known bool) {
	ll := tl.byChan[alias]
	best := -1
	for i, s := range ll {
		if s.fromPs < atPs || (s.fromPs == atPs && s.fromSeq < edgeSeq) {
			best = i
			continue
		}
		break
	}
	if best < 0 {
		return 0, -1, false
	}
	return ll[best].level, ll[best].fromSeq, true
}

// levelAfter is the level at an instant strictly after edgeSeq.
func (tl *timeline) levelAfter(alias string, atPs int64, edgeSeq int) (level int, known bool) {
	ll := tl.byChan[alias]
	best := -1
	for i, s := range ll {
		if s.fromPs < atPs || (s.fromPs == atPs && s.fromSeq <= edgeSeq) {
			best = i
			continue
		}
		break
	}
	if best < 0 {
		return 0, false
	}
	return ll[best].level, true
}

// clockEdges returns logical active/inactive clock edge events ordered on
// time. If the timeline provides a starting level for the clock, the leading
// rest phase is included with fromSeq -1.
func (tl *timeline) channelEdges(alias string) []model.Edge {
	out := make([]model.Edge, len(tl.byChan[alias]))
	ll := tl.byChan[alias]
	for i, s := range ll {
		out[i] = tl.edgeBySeq[s.fromSeq]
	}
	return out
}

// gapAt reports whether time t falls inside an imported data gap.
func (tl *timeline) gapAt(t int64) *model.Gap {
	for i := range tl.cap.Gaps {
		g := &tl.cap.Gaps[i]
		if g.OK && t >= g.FromPs && t < g.ToPs {
			return g
		}
	}
	return nil
}

func (tl *timeline) firstEdgeSeq(aliases ...string) int {
	best := -1
	for _, a := range aliases {
		if ll, ok := tl.byChan[a]; ok && len(ll) > 0 {
			s := ll[0].fromSeq
			if best < 0 || s < best {
				best = s
			}
		}
	}
	return best
}

func (tl *timeline) lastEdgeSeq(aliases ...string) int {
	best := -1
	for _, a := range aliases {
		if ll, ok := tl.byChan[a]; ok && len(ll) > 0 {
			s := ll[len(ll)-1].fromSeq
			if s > best {
				best = s
			}
		}
	}
	return best
}

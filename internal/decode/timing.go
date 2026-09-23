package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// levelAt returns the logic level of channel alias ch at instant t and
// whether the level changes at the exact same instant (a boundary sample).
// Initial level before the first edge is 0.
func (cc *calc) levelAt(ch string, t int64) (level int, simultaneous bool) {
	list := cc.level[ch]
	lvl := 0
	simul := false
	for _, el := range list {
		if !el.ok {
			continue
		}
		if el.t < t {
			lvl = el.level
			continue
		}
		if el.t == t {
			simul = true
			lvl = el.level
		}
		break
	}
	return lvl, simul
}

// boundaryLevel resolves an exactly-simultaneous data/clock transition using
// the configured boundary policy. The ambiguity is also surfaced as a
// candidate elsewhere; this returns the preferred value only.
func (cc *calc) boundaryLevel(ch string, t int64, postLevel int) int {
	if cc.params.BoundaryPolicy == "v2" {
		if pre, simul := cc.levelAtPre(ch, t); simul {
			return pre
		}
	}
	return postLevel
}

// levelAtPre returns the level strictly before t.
func (cc *calc) levelAtPre(ch string, t int64) (int, bool) {
	list := cc.level[ch]
	lvl := 0
	simul := false
	for _, el := range list {
		if !el.ok {
			continue
		}
		if el.t < t {
			lvl = el.level
			continue
		}
		if el.t == t {
			simul = true
		}
		break
	}
	return lvl, simul
}

// mergedEvent is one channel transition on the global timeline.
type mergedEvent struct {
	t       int64
	channel string
	edge    model.Edge
	level   int
}

// mergeTimeline returns the deterministic global ordering of a bus's channel
// edges. Simultaneous edges are ordered by channel alias so equivalent inputs
// always produce the same sequence and therefore the same conclusions.
func (cc *calc) mergeTimeline(channels ...string) []mergedEvent {
	var evs []mergedEvent
	for _, ch := range channels {
		for _, e := range cc.edgesByChan[ch] {
			if !e.Time.OK {
				continue
			}
			evs = append(evs, mergedEvent{t: e.Time.Value, channel: ch, edge: e, level: e.Level})
		}
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].channel < evs[j].channel
	})
	return evs
}

// identify assigns stable, run-scoped identifiers and global byte indexes.
func (cc *calc) identify(run *model.Run) {
	for i := range run.Bytes {
		b := &run.Bytes[i]
		b.Index = i
		if b.ByteID == "" {
			b.ByteID = model.ScopedID(run.RunID, "byte",
				fmt.Sprintf("%s|%d|%d", b.BusID, i, b.Range.StartPS))
		}
	}
	for ti := range run.Transactions {
		t := &run.Transactions[ti]
		if t.TxnID == "" {
			t.TxnID = model.ScopedID(run.RunID, "txn",
				fmt.Sprintf("%s|%d|%d", t.BusID, t.Ordinal, t.Range.StartPS))
		}
		for si := range t.Segments {
			s := &t.Segments[si]
			if s.SegmentID == "" {
				s.SegmentID = model.ScopedID(run.RunID, "seg",
					fmt.Sprintf("%s|%d|%d|%s", t.BusID, t.Ordinal, s.Range.StartPS, s.CS+s.Device))
			}
		}
	}
	for i := range run.Candidates {
		c := &run.Candidates[i]
		if c.CandidateID == "" {
			c.CandidateID = model.ScopedID(run.RunID, "cand",
				fmt.Sprintf("%s|%s|%d|%s", c.Kind, c.TargetID, i, c.Label))
		}
	}
}

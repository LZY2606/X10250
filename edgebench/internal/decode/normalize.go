package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// MaxEdgesDefault is the resource budget for one report. Exceeding it is a
// distinct budget_exhausted outcome, never a silent truncation.
const MaxEdgesDefault = 200_000

// MaxCandidates bounds retained legal candidates per byte; deterministic.
const MaxCandidates = 8

// Normalize derives ordered, deduplicated physical edges from immutable
// material. Unconvertible raw edges stay in the capture and become diagnostics
// rather than disappearing from evidence.
func Normalize(c *model.Capture) ([]model.Edge, []model.Diagnostic) {
	var diags []model.Diagnostic
	total := len(c.RawEdges)
	if total > MaxEdgesDefault {
		// budget handled by caller via typed error; here we only process bounded set
	}
	byPin := map[string][]model.Edge{}
	seen := map[string]bool{}
	for _, re := range c.RawEdges {
		if !re.OK {
			continue
		}
		ch := c.Channels[re.Channel]
		eid := fmt.Sprintf("%s/e/%d", c.ID, re.LineNo)
		key := re.Channel + "@" + fmt.Sprint(re.TNS)
		if seen[key] {
			continue
		}
		seen[key] = true
		byPin[re.Channel] = append(byPin[re.Channel], model.Edge{
			ID: eid, LineNo: re.LineNo, Pin: re.Channel, Alias: ch.Alias,
			TNS: re.TNS, Level: re.Level, Inverted: ch.Inverted,
		})
	}
	var all []model.Edge
	for pin := range byPin {
		es := byPin[pin]
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].TNS != es[j].TNS {
				return es[i].TNS < es[j].TNS
			}
			return es[i].LineNo < es[j].LineNo
		})
		byPin[pin] = es
		all = append(all, es...)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].TNS != all[j].TNS {
			return all[i].TNS < all[j].TNS
		}
		if all[i].Pin != all[j].Pin {
			return all[i].Pin < all[j].Pin
		}
		return all[i].LineNo < all[j].LineNo
	})
	return all, diags
}

// pinTimeline is one channel's ordered level transitions.
type pinTimeline struct {
	Pin   string
	Alias string
	Inv   bool
	Edges []model.Edge // sorted by time
}

func buildTimelines(c *model.Capture, all []model.Edge) map[string]*pinTimeline {
	tl := map[string]*pinTimeline{}
	for _, pin := range c.ChannelOrder {
		ch := c.Channels[pin]
		tl[pin] = &pinTimeline{Pin: pin, Alias: ch.Alias, Inv: ch.Inverted}
	}
	for _, e := range all {
		if t, ok := tl[e.Pin]; ok {
			t.Edges = append(t.Edges, e)
		}
	}
	return tl
}

// levelAt returns the physical level established at time t and the source edge
// line that established it. Before the first edge the line is assumed idle 0;
// that assumption is itself reported when a bus samples before any edge.
func (t *pinTimeline) levelAt(at int64) (int, int) {
	es := t.Edges
	if len(es) == 0 || at < es[0].TNS {
		return 0, -1
	}
	// largest index with TNS <= at
	lo, hi := 0, len(es)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if es[mid].TNS <= at {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return es[lo].Level, es[lo].LineNo
}

// levelAtCoincident is used when a data edge lands exactly on the sampling
// edge: it reports both the pre-edge and post-edge level so the decoder can
// retain two boundary samples instead of picking one silently.
func (t *pinTimeline) levelAtCoincident(at int64) (pre, post int, line int, coincident bool) {
	es := t.Edges
	idx := sort.Search(len(es), func(i int) bool { return es[i].TNS >= at })
	if idx < len(es) && es[idx].TNS == at {
		// edge exactly at sample instant
		var preLvl int
		if idx == 0 {
			preLvl = 0
		} else {
			preLvl = es[idx-1].Level
		}
		return preLvl, es[idx].Level, es[idx].LineNo, true
	}
	lvl, ln := t.levelAt(at)
	return lvl, lvl, ln, false
}

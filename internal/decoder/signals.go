package decoder

import (
	"sort"

	"edgebench/internal/domain"
)

// RulesVersion identifies the rule set used to derive results. Stored on
// every derived object so history can be restored to the exact rules.
const RulesVersion = "edgebench-rules-1.0.0"

const (
	gapPolicySplit = "split_transaction"
	stretchPolicy  = "hold_until_release"
	repeatPolicy   = "continue_transaction"
	missingPolicy  = "retain_candidates"
	multiCSPolicy  = "separate_device_transaction"
)

// logicalLevel applies the channel inversion recorded by the probe.
func logicalLevel(ch *domain.Channel, physicalLevel int) int {
	if ch != nil && ch.Inverted {
		return physicalLevel ^ 1
	}
	return physicalLevel
}

// ev is an in-memory edge after inversion.
type ev struct {
	id    string
	alias string
	level int
	ps    int64
	seq   int
}

// signal builds a time-ordered, deduplicated logical view of one channel.
func signal(ch *domain.Channel, edges []domain.Edge) []ev {
	out := make([]ev, 0, len(edges))
	for _, e := range edges {
		if ch != nil && e.Alias != ch.Alias {
			continue
		}
		if !e.TimeValid {
			continue
		}
		out = append(out, ev{id: e.ID, alias: e.Alias, level: logicalLevel(ch, e.Level), ps: e.TimePS, seq: e.Seq})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ps != out[j].ps {
			return out[i].ps < out[j].ps
		}
		return out[i].seq < out[j].seq
	})
	// Collapse edges that do not change logical level (e.g. a glitch that
	// returns before the next sample). Identity order is preserved.
	kept := out[:0]
	for _, e := range out {
		if n := len(kept); n > 0 && kept[n-1].level == e.level {
			continue
		}
		kept = append(kept, e)
	}
	return kept
}

// levelAt returns the logical level on s at time ps using the given initial
// level before the first edge.
func levelAt(s []ev, ps int64, initial int) (int, string) {
	lvl := initial
	var lastID string
	for _, e := range s {
		if e.ps <= ps {
			lvl = e.level
			lastID = e.id
		} else {
			break
		}
	}
	return lvl, lastID
}

// firstLevel infers the idle level before the first transition: for an
// active/inverted-independent reconstruction we assume idle high for
// open-drain I2C lines; callers may override per bus.
func firstLevel(s []ev, idle int) int {
	if len(s) == 0 {
		return idle
	}
	return s[0].level ^ 1
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func findChan(channels map[string]*domain.Channel, alias string) *domain.Channel {
	return channels[alias]
}

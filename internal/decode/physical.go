package decode

import (
	"sort"

	"edgebench/internal/domain"
	"edgebench/internal/identity"
)

// Trans is a physical (post-inversion) transition.
type Trans struct {
	EdgeID    string
	Channel   string
	Ps        int64
	SampleIdx int64
	Level     int // physical level
}

// PhysicalTimeline indexes transitions per logical role channel.
type PhysicalTimeline struct {
	capID    string
	byCh     map[string][]Trans
	byID     map[string]Trans
	interval int64
}

func BuildPhysical(c *domain.Capture) (*PhysicalTimeline, []*domain.Diagnostic) {
	pt := &PhysicalTimeline{
		capID:    c.ID,
		byCh:     map[string][]Trans{},
		byID:     map[string]Trans{},
		interval: c.SampleIntervalPs,
	}
	inverted := map[string]bool{}
	for _, ch := range c.Channels {
		inverted[ch.ID] = ch.Inverted
	}
	var diags []*domain.Diagnostic
	for _, e := range c.Edges {
		if !e.At.Valid {
			diags = append(diags, &domain.Diagnostic{
				Code: "UNCONVERTIBLE_VALUE", Severity: "error",
				Position: "edge " + e.ID + " (" + e.Channel + ")",
				Message:  "edge time cannot be normalized to the picosecond axis: " + e.At.Raw,
			})
			continue
		}
		lvl := e.Level
		if inverted[e.Channel] {
			lvl = 1 - lvl
		}
		tr := Trans{EdgeID: e.ID, Channel: e.Channel, Ps: e.At.NormPs, SampleIdx: e.SampleIdx, Level: lvl}
		pt.byCh[e.Channel] = append(pt.byCh[e.Channel], tr)
		pt.byID[e.ID] = tr
	}
	for ch := range pt.byCh {
		sort.SliceStable(pt.byCh[ch], func(i, j int) bool {
			if pt.byCh[ch][i].Ps != pt.byCh[ch][j].Ps {
				return pt.byCh[ch][i].Ps < pt.byCh[ch][j].Ps
			}
			return pt.byCh[ch][i].EdgeID < pt.byCh[ch][j].EdgeID
		})
	}
	return pt, diags
}

// ChannelByRole returns the channel ID carrying a role (e.g. SCL, CS1).
func ChannelByRole(c *domain.Capture, role string) (string, bool) {
	for _, ch := range c.Channels {
		if ch.Role == role {
			return ch.ID, true
		}
	}
	return "", false
}

// ChannelsByRolePrefix returns channels whose role starts with prefix (CS*).
func ChannelsByRolePrefix(c *domain.Capture, prefix string) []domain.Channel {
	var out []domain.Channel
	for _, ch := range c.Channels {
		if len(ch.Role) >= len(prefix) && ch.Role[:len(prefix)] == prefix {
			out = append(out, ch)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// Transitions returns the physical transition list for a channel.
func (p *PhysicalTimeline) Transitions(ch string) []Trans { return p.byCh[ch] }

func (p *PhysicalTimeline) Lookup(edgeID string) (Trans, bool) {
	tr, ok := p.byID[edgeID]
	return tr, ok
}

// LevelAt returns the bus level at time ps. Bus idles high when the first
// known level is high; when no transition precedes ps the first observed level
// is assumed (digital captures start from idle).
func (p *PhysicalTimeline) LevelAt(ch string, ps int64) int {
	es := p.byCh[ch]
	if len(es) == 0 {
		return 1
	}
	level := es[0].Level
	for _, e := range es {
		if e.Ps <= ps {
			level = e.Level
		} else {
			break
		}
	}
	return level
}

// EdgeWindow lists transitions on ch within [from,to].
func (p *PhysicalTimeline) EdgeWindow(ch string, from, to int64) []Trans {
	var out []Trans
	for _, e := range p.byCh[ch] {
		if e.Ps >= from && e.Ps <= to {
			out = append(out, e)
		}
	}
	return out
}

// NearestDataEdge returns the closest transition on dataCh to samplePs and the
// absolute distance; used for boundary-sampling detection.
func (p *PhysicalTimeline) NearestDataEdge(dataCh string, samplePs int64) (Trans, int64, bool) {
	var best *Trans
	var dist int64 = 1<<62
	for _, e := range p.byCh[dataCh] {
		d := e.Ps - samplePs
		if d < 0 {
			d = -d
		}
		if d < dist {
			dist = d
			e2 := e
			best = &e2
		}
	}
	if best == nil {
		return Trans{}, 0, false
	}
	return *best, dist, true
}

func (p *PhysicalTimeline) IntervalPs() int64 { return p.interval }

// InputSummary builds the checkable digest stored on every generation.
func InputSummary(c *domain.Capture) domain.InputSummary {
	ids := make([]string, 0, len(c.Channels))
	for _, ch := range c.Channels {
		ids = append(ids, ch.ID)
	}
	sort.Strings(ids)
	type edgeKey struct {
		Ch    string
		AtPs  int64
		Valid bool
		Level int
		Raw   string
	}
	keys := make([]edgeKey, 0, len(c.Edges))
	var minP, maxP int64
	for i, e := range c.Edges {
		if i == 0 || e.At.NormPs < minP {
			minP = e.At.NormPs
		}
		if e.At.NormPs > maxP {
			maxP = e.At.NormPs
		}
		keys = append(keys, edgeKey{Ch: e.Channel, AtPs: e.At.NormPs, Valid: e.At.Valid, Level: e.Level, Raw: e.At.Raw})
	}
	return domain.InputSummary{
		EdgeCount:        len(c.Edges),
		ChannelIDs:       ids,
		SampleIntervalPs: c.SampleIntervalPs,
		TimeMinPs:        minP,
		TimeMaxPs:        maxP,
		EdgeHash:         identity.Derive("edges", keys),
	}
}

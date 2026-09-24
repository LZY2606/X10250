// Package decode reassembles I2C, SPI and custom synchronous transactions
// from normalized digital edges. The rules run entirely on the server and
// are unit tested directly; the browser only renders their conclusions.
package decode

import (
	"sort"

	"edgebench/internal/domain"
)

// Signal is a step function of one channel's logical level. The level at
// instant t is right-continuous: edge at t presents the NEW level.
type Signal struct {
	Chan   string
	Idle   int
	edges  []*domain.Edge
}

func (s *Signal) At(t int64) int {
	level := s.Idle
	for _, e := range s.edges {
		if *e.Time.NS <= t {
			level = *e.Level
		} else {
			break
		}
	}
	return level
}

// Crossing finds the first edge of ch strictly after after at which the
// level becomes want (-1 = either edge). Returns nil if none.
func (s *Signal) crossing(after int64, want int) *domain.Edge {
	for _, e := range s.edges {
		if *e.Time.NS <= after {
			continue
		}
		if want < 0 || *e.Level == want {
			return e
		}
	}
	return nil
}

func (s *Signal) firstEdge() *domain.Edge {
	if len(s.edges) == 0 {
		return nil
	}
	return s.edges[0]
}

// Clock half-period enumeration helpers.
type ed struct {
	e *domain.Edge
}

// Timeline holds all bus signals and exposes scanning primitives.
type Timeline struct {
	signals map[string]*Signal
	edges   []*domain.Edge
}

// Build constructs per-channel signals. Only channels in keep are built.
func Build(m *domain.Materials, edges []*domain.Edge, keep map[string]bool) *Timeline {
	tl := &Timeline{signals: map[string]*Signal{}, edges: edges}
	chans := m.ChannelByID()
	per := map[string][]*domain.Edge{}
	for _, e := range edges {
		if keep == nil || keep[e.Chan] {
			per[e.Chan] = append(per[e.Chan], e)
		}
	}
	for cid, es := range per {
		ch := chans[cid]
		idle := 0
		if ch != nil && ch.Idle != nil {
			idle = *ch.Idle
		}
		sort.SliceStable(es, func(i, j int) bool { return *es[i].Time.NS < *es[j].Time.NS })
		tl.signals[cid] = &Signal{Chan: cid, Idle: idle, edges: es}
	}
	return tl
}

func (tl *Timeline) Signal(chanID string) *Signal {
	s := tl.signals[chanID]
	if s == nil {
		return &Signal{Chan: chanID, Idle: 0}
	}
	return s
}

// span returns the observed [min,max] edge time over the listed channels.
func (tl *Timeline) span(chanIDs []string) (int64, int64, bool) {
	var mn, mx int64
	first := true
	for _, cid := range chanIDs {
		s := tl.signals[cid]
		if s == nil || len(s.edges) == 0 {
			continue
		}
		lo := *s.edges[0].Time.NS
		hi := *s.edges[len(s.edges)-1].Time.NS
		if first || lo < mn {
			mn = lo
		}
		if hi > mx {
			mx = hi
		}
		first = false
	}
	return mn, mx, !first
}

func i64(v int64) *int64 { return &v }

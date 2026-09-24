package store

import (
	"edgebench/internal/domain"
	"edgebench/internal/units"
)

// PatchMutation changes exactly one direct source. It is stored verbatim
// in the event so a reviewer can replay and attribute the difference.
type PatchMutation interface {
	Kind() string
	BusID() string
	Apply(chans []domain.Channel, edges []domain.Edge) error
}

// EdgeTimeMutation rewrites one edge timestamp (its raw representation is
// preserved too). This is a single direct source change.
type EdgeTimeMutation struct {
	Seq      int    `json:"seq"`
	NewValue string `json:"newValue"`
	NewUnit  string `json:"newUnit"`
	Reason   string `json:"reason"`
}

func (m EdgeTimeMutation) Kind() string  { return "edge_time" }
func (m EdgeTimeMutation) BusID() string { return "" }
func (m EdgeTimeMutation) Apply(chans []domain.Channel, edges []domain.Edge) error {
	if m.Seq < 0 || m.Seq >= len(edges) {
		return semantic("mutation.seq", "edge seq out of range")
	}
	q := units.Parse(m.NewValue, m.NewUnit, units.KindTime)
	e := &edges[m.Seq]
	ps, ok := units.Picoseconds(q)
	e.TimePS, e.TimeValid = ps, ok
	e.RawTime, e.RawUnit = m.NewValue, m.NewUnit
	e.Raw = map[string]any{"time": RawQuantity{Value: m.NewValue, Unit: m.NewUnit}, "level": e.Level, "patch": m.Reason}
	return nil
}

// ChannelInversionMutation flips one probe's level inversion.
type ChannelInversionMutation struct {
	Alias    string `json:"alias"`
	Inverted bool   `json:"inverted"`
	Bus      string `json:"bus"`
}

func (m ChannelInversionMutation) Kind() string  { return "channel_inversion" }
func (m ChannelInversionMutation) BusID() string { return m.Bus }
func (m ChannelInversionMutation) Apply(chans []domain.Channel, edges []domain.Edge) error {
	for i := range chans {
		if chans[i].Alias == m.Alias {
			chans[i].Inverted = m.Inverted
			if chans[i].Raw == nil {
				chans[i].Raw = map[string]any{}
			}
			chans[i].Raw["inverted"] = m.Inverted
			return nil
		}
	}
	return semantic("mutation.alias", "unknown channel "+m.Alias)
}

// DeleteEdgeMutation removes exactly one edge (used to manufacture a
// missing-clock-edge condition).
type DeleteEdgeMutation struct {
	Seq int `json:"seq"`
}

func (m DeleteEdgeMutation) Kind() string  { return "delete_edge" }
func (m DeleteEdgeMutation) BusID() string { return "" }
func (m DeleteEdgeMutation) Apply(chans []domain.Channel, edges []domain.Edge) error {
	if m.Seq < 0 || m.Seq >= len(edges) {
		return semantic("mutation.seq", "edge seq out of range")
	}
	copy(edges[m.Seq:], edges[m.Seq+1:])
	edges[len(edges)-1] = domain.Edge{}
	*(&edges) = edges[:len(edges)-1]
	return nil
}

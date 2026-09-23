package decode

import (
	"sort"
	"strings"
)

// DecoderVersion is the version of the service-side bus reconstruction rules.
// It is retained on every capture and stamped on every derived candidate.
const DecoderVersion = "edgebench-decoder-1.0.0"

// levelEvent is one edge in the global, time-sorted stream.
type levelEvent struct {
	edge    Edge
	ch      *ChannelSpec
	role    string
	t       int64
	logic   int
	valid   bool
}

type edgeStream struct {
	cap    *Capture
	evs    []levelEvent
	byID   map[string]int
	roleCh map[string]*ChannelSpec
	diag   []Diagnostic
}

func buildStream(c *Capture) *edgeStream {
	st := &edgeStream{
		cap:    c,
		byID:   map[string]int{},
		roleCh: map[string]*ChannelSpec{},
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.Role != "" {
			st.roleCh[ch.Role] = ch
		}
	}
	rate := 0.0
	if c.Params.SampleRate.NormalValid {
		rate = c.Params.SampleRate.Normal
	}
	for _, e := range c.Edges {
		ch := c.ChannelByAlias(e.Channel)
		le := levelEvent{edge: e, ch: ch}
		if ch != nil {
			le.role = ch.Role
			logic := e.LevelRaw
			if ch.Inverted {
				logic = 1 - logic
			}
			le.logic = logic
		}
		if !e.Time.NormalValid {
			// kept in place; not used by reconstruction
			st.diag = append(st.diag, Diagnostic{
				Code: "unconvertible_time", Path: "edges[" + itoa(e.Seq) + "].time",
				Message: "edge time could not be normalized; retained raw and excluded from decode",
				Severity: "warning",
			})
			continue
		}
		q := ParseTimeQuantity(e.Time.Raw, e.Time.Unit, c.Params.TimeBase, rate)
		le.t = int64(q.Normal + 0.5)
		le.valid = true
		if ch == nil {
			st.diag = append(st.diag, Diagnostic{
				Code: "unknown_channel", Path: "edges[" + itoa(e.Seq) + "].channel",
				Message: "edge references unknown channel alias: " + e.Channel, Severity: "error",
			})
		}
		st.evs = append(st.evs, le)
	}
	sort.SliceStable(st.evs, func(i, j int) bool {
		if st.evs[i].t != st.evs[j].t {
			return st.evs[i].t < st.evs[j].t
		}
		if st.evs[i].edge.Channel != st.evs[j].edge.Channel {
			return st.evs[i].edge.Channel < st.evs[j].edge.Channel
		}
		return st.evs[i].edge.Seq < st.evs[j].edge.Seq
	})
	for i := range st.evs {
		st.byID[st.evs[i].edge.ID] = i
	}
	return st
}

// levelAt returns logic level of a role channel at time t using edges and the
// retained initial state.
func (s *edgeStream) levelAt(role string, t int64) int {
	ch := s.roleCh[role]
	if ch == nil {
		return 0
	}
	lvl := 0
	if ch.InitialRaw != nil {
		lvl = *ch.InitialRaw
		if ch.Inverted {
			lvl = 1 - lvl
		}
	}
	for i := range s.evs {
		ev := &s.evs[i]
		if ev.role != role || !ev.valid || ev.t > t {
			continue
		}
		lvl = ev.logic
	}
	return lvl
}

// edgesBetween returns level events for a role within [from,to].
func (s *edgeStream) edgesBetween(role string, from, to int64) []levelEvent {
	var out []levelEvent
	for i := range s.evs {
		ev := &s.evs[i]
		if ev.role == role && ev.valid && ev.t >= from && ev.t <= to {
			out = append(out, *ev)
		}
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func uniqEdgeIDs(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// summary computes a deterministic summary of the decode inputs.
func (s *edgeStream) summary(c *Capture) InputSummary {
	sum := InputSummary{
		CaptureHash: c.ID,
		ParamsHash:  CanonicalHash("ph_", c.Params),
		EdgeCount:   len(c.Edges),
	}
	for i := range s.evs {
		if !s.evs[i].valid {
			continue
		}
		sum.ValidEdges++
		if sum.RangeStart == 0 || s.evs[i].t < sum.RangeStart {
			sum.RangeStart = s.evs[i].t
		}
		if s.evs[i].t > sum.RangeEnd {
			sum.RangeEnd = s.evs[i].t
		}
	}
	return sum
}

// Decode reconstructs bus transactions from retained capture material. It is
// pure: same input revision + decoder version => same ordered result.
func Decode(c *Capture) (*DecodeResult, error) {
	if c == nil {
		return nil, &SemanticError{Diagnostics: []Diagnostic{{Code: "missing_capture", Path: "", Message: "capture is null"}}}
	}
	st := buildStream(c)
	res := &DecodeResult{
		CaptureID:    c.ID,
		Revision:     c.Revision,
		DecoderVer:   c.DecoderVer,
		InputSummary: st.summary(c),
	}
	res.Diagnostics = append(res.Diagnostics, c.Diagnostics...)
	res.Diagnostics = append(res.Diagnostics, st.diag...)

	switch strings.ToLower(c.Protocol) {
	case "i2c":
		if err := decodeI2C(st, res); err != nil {
			return nil, err
		}
	case "spi":
		if err := decodeSPI(st, res); err != nil {
			return nil, err
		}
	case "sync":
		if err := decodeSync(st, res); err != nil {
			return nil, err
		}
	default:
		return nil, &SemanticError{Diagnostics: []Diagnostic{{
			Code: "unknown_protocol", Path: "protocol", Message: "unsupported protocol: " + c.Protocol, Severity: "error",
		}}}
	}
	return res, nil
}

// DecodePack convenience used by tests/tools.
func DecodePack(captures []*Capture) ([]*DecodeResult, error) {
	var out []*DecodeResult
	for _, c := range captures {
		r, err := Decode(c)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

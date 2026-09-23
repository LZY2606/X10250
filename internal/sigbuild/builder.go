// Package sigbuild constructs deterministic digital-channel capture payloads
// with exact edge timing. It is used by the offline case-pack generator and
// by the decoder acceptance tests, so the fixtures are real edge material -
// not abstracted tables.
package sigbuild

import (
	"encoding/json"
	"strconv"

	"edgebench/internal/ingest"
)

// Config describes one generated capture's immutable probe material.
type Config struct {
	ID             string
	Name           string
	Bus            string
	TimeUnit       string // "ns" or "us" - the original representation unit
	SampleRate     string // original token, e.g. "5 MS/s"
	DecoderVersion string
	TimeBase       string
}

// Builder accumulates per-channel edge tokens at nanosecond resolution.
type Builder struct {
	cfg     Config
	chans   map[string]*chanDef
	order   []string
	t       float64
	rawMeta map[string]json.RawMessage
}

type chanDef struct {
	id, alias, role string
	inverted        bool
	clkPol          string
	level           int // current logical level
	start           int
	edges           []ingest.EdgeTok
}

// New creates a builder and declares channels in the given order.
func New(cfg Config, chans ...ChanSpec) *Builder {
	if cfg.TimeUnit == "" {
		cfg.TimeUnit = "ns"
	}
	b := &Builder{cfg: cfg, chans: map[string]*chanDef{}, rawMeta: map[string]json.RawMessage{}}
	for _, c := range chans {
		d := &chanDef{id: c.ID, alias: c.Alias, role: c.Role, inverted: c.Inverted,
			clkPol: c.ClockPolarity, level: c.StartLevel, start: c.StartLevel}
		b.chans[c.ID] = d
		b.order = append(b.order, c.ID)
	}
	return b
}

// ChanSpec declares one channel.
type ChanSpec struct {
	ID, Alias, Role string
	Inverted        bool
	ClockPolarity   string
	StartLevel      int
}

// T returns current builder time in ns.
func (b *Builder) T() float64 { return b.t }

// Advance moves time without any edge (produces a real idle gap).
func (b *Builder) Advance(ns float64) { b.t += ns }

func (b *Builder) set(chID string, level int) {
	ch := b.chans[chID]
	if ch.level == level {
		return
	}
	ch.level = level
	ch.edges = append(ch.edges, ingest.EdgeTok{T: b.render(b.t), L: b.rawLevel(ch, level)})
}

func (b *Builder) rawLevel(ch *chanDef, logical int) string {
	if ch.inverted {
		if logical == 1 {
			return "0"
		}
		return "1"
	}
	return strconv.Itoa(logical)
}

func (b *Builder) render(ns float64) string {
	switch b.cfg.TimeUnit {
	case "us", "µs":
		return trimFloat(ns/1000) + "us"
	case "ms":
		return trimFloat(ns/1e6) + "ms"
	case "s":
		return trimFloat(ns/1e9) + "s"
	default:
		return trimFloat(ns) + "ns"
	}
}

func trimFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	return s
}

// Payload produces the raw import payload, including an initial state edge at
// t=0 for every channel so the initial level is itself evidence.
func (b *Builder) Payload() *Payload {
	cp := ingest.CapturePayload{
		ID: b.cfg.ID, Name: b.cfg.Name, Bus: b.cfg.Bus,
		TimeBase: b.cfg.TimeBase, TimeUnit: b.cfg.TimeUnit,
		SampleRate: b.cfg.SampleRate, DecoderVersion: b.cfg.DecoderVersion,
		RawProps: b.rawMeta,
	}
	if b.cfg.TimeBase == "" {
		cp.TimeBase = "t0=trigger"
	}
	for _, id := range b.order {
		ch := b.chans[id]
		cp.Channels = append(cp.Channels, ingest.ChannelPayload{
			ID: ch.id, Alias: ch.alias, Role: ch.role,
			Inverted: ch.inverted, ClockPolarity: ch.clkPol,
		})
	}
	if cp.Edges == nil {
		cp.Edges = map[string][]ingest.EdgeTok{}
	}
	for _, id := range b.order {
		ch := b.chans[id]
		list := []ingest.EdgeTok{{T: b.render(0), L: b.rawLevel(ch, ch.start)}}
		list = append(list, ch.edges...)
		cp.Edges[id] = list
	}
	return &Payload{C: &cp}
}

// Payload wraps ingest.Payload with the schema label.
type Payload struct{ C *ingest.CapturePayload }

// Marshal renders the JSON an importer would receive from the hardware team.
func (p *Payload) Marshal() ([]byte, error) {
	return json.MarshalIndent(struct {
		SchemaVersion string                 `json:"schema_version"`
		Capture       ingest.CapturePayload  `json:"capture"`
	}{"edgebench.capture/v1", *p.C}, "", "  ")
}

// ChannelOrder exposes deterministic channel order for tests.
func (b *Builder) ChannelOrder() []string { return b.order }

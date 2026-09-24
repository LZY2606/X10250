// Package wavebuild constructs deterministic digital captures for the
// offline case pack and the acceptance tests. Edges are placed at exact
// picosecond coordinates; raw strings preserve the probe representation.
package wavebuild

import (
	"fmt"
	"sort"
	"time"

	"edgebench/internal/domain"
	"edgebench/internal/identity"
	"edgebench/internal/quantity"
)

const (
	DefaultSampleRateMHz = 100
	DefaultIntervalPs    = 10_000
)

// I2C role channel IDs.
const (
	IDSCL = "SCL"
	IDSDA = "SDA"
)

// SPI role channel IDs. CS lines: CS0, CS1...
const (
	IDSCLK = "SCLK"
	IDMOSI = "MOSI"
	IDMISO = "MISO"
)

// Custom synchronous bus roles.
const (
	IDCCLK  = "CCLK"
	IDCDATA = "CDATA"
	IDCSYNC = "CSYNC"
)

type rawEdge struct {
	ch    string
	atPs  int64
	level int
}

// Timeline builds one capture on a shared picosecond axis.
type Timeline struct {
	captureID string
	edges     map[string][]rawEdge // ch -> edges (sorted before flush)
	channels  map[string]domain.Channel
	inverted  map[string]bool
	interval  int64
	baseIdx   int64
	extra     map[string]string // clock polarity hints
	diag      []*domain.Diagnostic
}

func New(captureID string) *Timeline {
	return &Timeline{
		captureID: captureID,
		edges:     map[string][]rawEdge{},
		channels:  map[string]domain.Channel{},
		inverted:  map[string]bool{},
		interval:  DefaultIntervalPs,
		extra:     map[string]string{},
	}
}

func (t *Timeline) WithIntervalPs(ps int64) *Timeline { t.interval = ps; return t }
func (t *Timeline) WithBaseSampleIndex(i int64) *Timeline { t.baseIdx = i; return t }

func (t *Timeline) Channel(id, alias, role string, inverted bool) *Timeline {
	th, _ := quantity.ParseVoltage("1.65V")
	t.channels[id] = domain.Channel{ID: id, Alias: alias, Role: role, Inverted: inverted, Threshold: th}
	t.inverted[id] = inverted
	return t
}

func (t *Timeline) ClockHint(bus, polarity string) *Timeline { t.extra[bus] = polarity; return t }

// Level applies the physical (post-inversion) level; stored raw level is
// flipped when the probe channel is marked inverted.
func (t *Timeline) Level(ch string, atPs int64, physLevel int) *Timeline {
	raw := physLevel
	if t.inverted[ch] {
		raw = 1 - physLevel
	}
	if _, ok := t.channels[ch]; !ok {
		t.Channel(ch, ch, ch, false)
	}
	t.edges[ch] = append(t.edges[ch], rawEdge{ch: ch, atPs: atPs, level: raw})
	return t
}

// KeepRaw records an edge in probe-raw coordinates without inversion handling.
func (t *Timeline) KeepRaw(ch string, atPs int64, rawLevel int) *Timeline {
	if _, ok := t.channels[ch]; !ok {
		th, _ := quantity.ParseVoltage("1.65V")
		t.channels[ch] = domain.Channel{ID: ch, Alias: ch, Role: ch, Inverted: false, Threshold: th}
	}
	t.edges[ch] = append(t.edges[ch], rawEdge{ch: ch, atPs: atPs, level: rawLevel})
	return t
}

// AddDiagnostic attaches an import-time diagnostic (e.g. unconvertible edge).
func (t *Timeline) AddDiagnostic(code, pos, msg string) *Timeline {
	t.diag = append(t.diag, &domain.Diagnostic{Code: code, Severity: "error", Position: pos, Message: msg})
	return t
}

// Build emits the immutable capture. Edges receive stable content identities.
func (t *Timeline) Build(version int, parentID, createdBy string) *domain.Capture {
	ids := make([]string, 0, len(t.channels))
	for id := range t.channels {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	chans := make([]domain.Channel, 0, len(ids))
	for _, id := range ids {
		chans = append(chans, t.channels[id])
	}

	var all []*domain.Edge
	perCh := map[string]int{}
	for _, id := range ids {
		es := t.edges[id]
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].atPs != es[j].atPs {
				return es[i].atPs < es[j].atPs
			}
			return es[i].level < es[j].level
		})
		// collapse duplicate same-level transitions at identical time
		clean := es[:0]
		for _, e := range es {
			if len(clean) > 0 && clean[len(clean)-1].atPs == e.atPs && clean[len(clean)-1].level == e.level {
				continue
			}
			clean = append(clean, e)
		}
		for idx, e := range clean {
			sampleIdx := t.baseIdx + e.atPs/t.interval
			id := identity.Derive("edge", t.captureID, id, idx, e.atPs, sampleIdx, e.level)
			all = append(all, &domain.Edge{
				ID: id, Channel: e.ch,
				At:        mustTime(e.atPs),
				SampleIdx: sampleIdx,
				Level:     e.level,
			})
			perCh[id] = idx
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].At.NormPs != all[j].At.NormPs {
			return all[i].At.NormPs < all[j].At.NormPs
		}
		if all[i].Channel != all[j].Channel {
			return all[i].Channel < all[j].Channel
		}
		return all[i].ID < all[j].ID
	})

	rateRaw := fmt.Sprintf("%d MHz", 1_000_000_000_000/t.interval/1_000_000)
	rate, _ := quantity.ParseRate(rateRaw)
	c := &domain.Capture{
		ID:                t.captureID,
		FormatVersion:     "edgebench.capture/v1",
		Version:           version,
		ParentID:          parentID,
		SampleRate:        rate,
		SampleIntervalPs:  t.interval,
		ClockPolarityHint: t.extra,
		Channels:          chans,
		Edges:             all,
		Diagnostics:       t.diag,
		CreatedBy:         createdBy,
		CreatedAt:         time.Now().UTC().Format(time.RFC3339),
	}
	return c
}

func mustTime(ps int64) domain.Quantity {
	q, d := quantity.ParseTime(quantity.FormatPs(ps))
	if d != nil {
		panic(d.Message)
	}
	return q
}

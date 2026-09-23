// Package decode reconstructs I2C, SPI and custom synchronous transactions from
// raw digital edges. Ambiguous situations keep every legal candidate instead
// of guessing; every judgment is machine-readable and edge-referenced.
package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

const DecoderID = "edgebench-decoder/1.0.0"

// MaxCandidates bounds candidate explosion; exceeding it is a distinct
// resource-budget outcome, not a silent truncation.
const MaxCandidates = 256

// physEdge is a normalized transition with a logical level.
type physEdge struct {
	t     float64
	level bool
	id    string
	ch    string
}

type timeline struct {
	cap   model.Capture
	probe map[string]model.Probe // alias -> probe
	byCh  map[string][]physEdge
	start map[string]bool // initial logical level before first edge
}

func buildTimeline(c model.Capture) *timeline {
	tl := &timeline{cap: c, probe: map[string]model.Probe{}, byCh: map[string][]physEdge{}, start: map[string]bool{}}
	for _, p := range c.Probes {
		tl.probe[p.Channel] = p
	}
	for _, e := range c.Edges {
		if !e.T.Ok {
			continue
		}
		lvl := e.Level == "1"
		if p, ok := tl.probe[e.Chan]; ok && p.Inverted {
			lvl = !lvl
		}
		tl.byCh[e.Chan] = append(tl.byCh[e.Chan], physEdge{t: e.T.V, level: lvl, id: e.ID, ch: e.Chan})
	}
	for ch := range tl.byCh {
		es := tl.byCh[ch]
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].t != es[j].t {
				return es[i].t < es[j].t
			}
			return es[i].id < es[j].id
		})
		if len(es) > 0 {
			tl.start[ch] = !es[0].level
		}
		tl.byCh[ch] = es
	}
	return tl
}

// levelAt returns logical level and the defining edge id at time t.
func (tl *timeline) levelAt(ch string, t float64) (bool, string) {
	es := tl.byCh[ch]
	idx := sort.Search(len(es), func(i int) bool { return es[i].t > t }) - 1
	if idx < 0 {
		return tl.start[ch], ""
	}
	return es[idx].level, es[idx].id
}

// edgesAt returns edges of ch within |dt| of t.
func (tl *timeline) edgesNear(ch string, t, dt float64) []physEdge {
	var out []physEdge
	for _, e := range tl.byCh[ch] {
		if absF(e.t-t) <= dt {
			out = append(out, e)
		}
	}
	return out
}

func edgesOfKind(es []physEdge, level bool) []physEdge {
	var out []physEdge
	for _, e := range es {
		if e.level == level {
			out = append(out, e)
		}
	}
	return out
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

const boundaryJitter = 1.5e-9 // seconds on the 10 ns seed grid

type engine struct {
	tl         *timeline
	params     model.Params
	cands      []model.Candidate
	judgments  []model.Judgment
	diags      []model.Diagnostic
	budgetLeft int
}

// aliasesForRole returns channel aliases assigned a role.
func (e *engine) aliasesForRole(role string) []string {
	var out []string
	for _, p := range e.tl.cap.Probes {
		if p.Role == role {
			out = append(out, p.Channel)
		}
	}
	sort.Strings(out)
	return out
}

func (e *engine) reserve(n int) bool {
	if e.budgetLeft < n {
		return false
	}
	e.budgetLeft -= n
	return true
}

func (e *engine) addCand(c model.Candidate) {
	c.ID = candID(c)
	for i := range c.Bytes {
		c.Bytes[i].UID = fmt.Sprintf("B-%s-%d", c.ID[:8], i)
	}
	for i := range c.Evidence {
		ev := c.Evidence[i]
		ev.UID = fmt.Sprintf("V-%s", ingest.Hash8(c.ID, ev.Kind, fmt.Sprintf("%.6f", ev.AtNs), joinRefs(ev.Edges)))
		c.Evidence[i] = ev
	}
	e.cands = append(e.cands, c)
}

func joinRefs(rs []string) string {
	out := ""
	for _, r := range rs {
		out += r + ","
	}
	return out
}

func candID(c model.Candidate) string {
	bs := ""
	for _, b := range c.Bytes {
		bs += b.ValueHex + b.Kind + b.Ack + "/"
	}
	vs := ""
	for _, v := range c.Evidence {
		vs += v.Kind + fmt.Sprintf("%.3f", v.AtNs)
	}
	return "C-" + ingest.Hash8(c.Bus, c.Mode, fmt.Sprintf("%d", c.FrameIdx), fmt.Sprintf("%.3f-%.3f", c.Valid.StartNs, c.Valid.EndNs), bs, vs, c.Status)
}

func ns(t float64) float64 { return t * 1e9 }

// InputSummary is a stable hash of normalized material plus parameter version.
func InputSummary(c model.Capture, pv int) string {
	parts := []string{fmt.Sprintf("pv%d", pv), c.SampleRate.Unit, fmt.Sprintf("%.6f", c.SampleRate.V)}
	for _, p := range c.Probes {
		parts = append(parts, p.Channel, p.Alias, p.InvertedRaw, p.Role, p.ClkPolRaw, p.Volt.Raw)
	}
	for _, ed := range c.Edges {
		parts = append(parts, ed.ID, ed.Chan, ed.Level, ed.T.Raw, fmt.Sprintf("%.9f", ed.T.V), fmt.Sprintf("%v", ed.T.Ok))
	}
	return "S-" + ingest.Hash8(parts...)
}

// Decode runs every applicable decoder. It never fabricates success:
// missing required channels is SEMANTIC_NO_SOLUTION, candidate explosion is
// BUDGET_EXHAUSTED (both distinct from ingest-time FORMAT_CORRUPT).
func Decode(c model.Capture, p model.Params, busHint string) (model.DecodeResult, error) {
	en := &engine{tl: buildTimeline(c), params: p, budgetLeft: MaxCandidates}
	roles := map[string]bool{}
	for _, pr := range c.Probes {
		roles[pr.Role] = true
	}
	buses := []string{}
	if busHint != "" {
		buses = append(buses, busHint)
	} else {
		if roles["scl"] && roles["sda"] {
			buses = append(buses, "i2c")
		}
		if roles["sck"] {
			buses = append(buses, "spi")
		}
		if roles["sync"] {
			buses = append(buses, "custom")
		}
	}
	if len(buses) == 0 {
		return model.DecodeResult{}, &ingest.Error{Code: ingest.ErrSemanticNoSolve,
			Message: "no decodable bus: probe roles do not identify i2c/spi/custom channels"}
	}
	for _, bus := range buses {
		var err *ingest.Error
		switch bus {
		case "i2c":
			err = en.decodeI2C()
		case "spi":
			err = en.decodeSPI()
		case "custom":
			err = en.decodeCustom()
		default:
			return model.DecodeResult{}, &ingest.Error{Code: ingest.ErrSemanticNoSolve,
				Message: "unknown bus hint: " + bus}
		}
		if err != nil {
			return model.DecodeResult{}, err
		}
	}
	sort.SliceStable(en.cands, func(i, j int) bool {
		a, b := en.cands[i], en.cands[j]
		if a.Valid.StartNs != b.Valid.StartNs {
			return a.Valid.StartNs < b.Valid.StartNs
		}
		if a.Bus != b.Bus {
			return a.Bus < b.Bus
		}
		if a.FrameIdx != b.FrameIdx {
			return a.FrameIdx < b.FrameIdx
		}
		if a.Mode != b.Mode {
			return a.Mode < b.Mode
		}
		return a.ID < b.ID
	})
	res := model.DecodeResult{
		BusHint:       busHint,
		Candidates:    en.cands,
		Judgments:     en.judgments,
		InputSummary:  InputSummary(c, p.Version),
		ParamsVersion: p.Version,
		DecoderID:     DecoderID,
		Diagnostics:   en.diags,
	}
	return res, nil
}

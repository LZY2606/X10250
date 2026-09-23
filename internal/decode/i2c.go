package decode

import (
	"sort"

	"edgetrace/internal/model"
)

// I2C evidence codes (machine-readable).
const (
	EvI2CStart         = "i2c.start"
	EvI2CRepeated      = "i2c.repeated_start"
	EvI2CStop          = "i2c.stop"
	EvI2CAck           = "i2c.ack"
	EvI2CNack          = "i2c.nack"
	EvI2CStretch       = "i2c.clock_stretch"
	EvI2CMissingEdge   = "i2c.missing_clock_edge"
	EvI2CGap           = "i2c.data_gap"
	EvI2CBoundary      = "i2c.boundary_sample"
	EvI2CCandidateKept = "i2c.candidate_retained"
)

type i2cHyp struct {
	key      string
	polarity string
	activeUp bool // true: data sampled on SCL rising
}

func decodeI2C(x *ctx) []model.Candidate {
	scl, sda := x.sig("scl"), x.sig("sda")
	if scl == nil || sda == nil {
		return nil
	}
	pol := x.par.ClockPolarity[x.bus.Name]
	if pol == "" {
		pol = "normal"
	}
	var hyps []i2cHyp
	switch pol {
	case "inverted":
		hyps = []i2cHyp{{key: "polarity=inverted", polarity: "inverted", activeUp: false}}
	case "unknown":
		hyps = []i2cHyp{
			{key: "polarity=normal", polarity: "normal", activeUp: true},
			{key: "polarity=inverted", polarity: "inverted", activeUp: false},
		}
	default:
		hyps = []i2cHyp{{key: "polarity=normal", polarity: "normal", activeUp: true}}
	}
	var out []model.Candidate
	for _, h := range hyps {
		c := x.decodeOneI2C(scl, sda, h)
		out = append(out, c)
	}
	return out
}

// i2cEvent is a merged time-ordered point of bus activity.
type i2cEvent struct {
	sample int64
	ns     model.Rat
	sclLvl int // level AFTER transition, -1 unchanged
	sdaLvl int
	sclIDs []string
	sdaIDs []string
}

func mergeI2CEvents(scl, sda *Sig) []i2cEvent {
	set := map[int64]*i2cEvent{}
	var samples []int64
	add := func(n int64, ch string, e model.Edge) {
		q := set[n]
		if q == nil {
			q = &i2cEvent{sample: n, sclLvl: -1, sdaLvl: -1, ns: *e.OffsetNs}
			set[n] = q
			samples = append(samples, n)
		}
		if ch == "scl" {
			q.sclLvl = e.Level
			q.sclIDs = append(q.sclIDs, e.ID)
		} else {
			q.sdaLvl = e.Level
			q.sdaIDs = append(q.sdaIDs, e.ID)
		}
	}
	for _, e := range scl.Edges() {
		add(e.Sample, "scl", e)
	}
	for _, e := range sda.Edges() {
		add(e.Sample, "sda", e)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	out := make([]i2cEvent, 0, len(samples))
	for _, n := range samples {
		out = append(out, *set[n])
	}
	// fill unchanged levels
	cl, dl := -1, -1
	for i := range out {
		if out[i].sclLvl == -1 {
			out[i].sclLvl = cl
		} else {
			cl = out[i].sclLvl
		}
		if out[i].sdaLvl == -1 {
			out[i].sdaLvl = dl
		} else {
			dl = out[i].sdaLvl
		}
	}
	return out
}

type i2cState struct {
	x        *ctx
	h        i2cHyp
	scl      *Sig
	sda      *Sig
	evs      []i2cEvent
	boundary string
	gap      model.Rat
	evid     []model.Evidence
	frames   []frame
	segs     []model.Segment
	txns     []model.Transaction
	bytes    []model.Byte
}

func (x *ctx) decodeOneI2C(scl, sda *Sig, h i2cHyp) model.Candidate {
	gap := x.par.GapToleranceNs
	if gap == nil {
		g := model.RatInt(50_000)
		gap = &g
	}
	st := &i2cState{
		x: x, h: h, scl: scl, sda: sda,
		evs:      mergeI2CEvents(scl, sda),
		boundary: x.par.SampleBoundary,
		gap:      *gap,
	}
	if st.boundary == "" {
		st.boundary = "last"
	}
	st.run()

	cand := model.Candidate{
		ID:           candID(x, "i2c", h.key),
		Bus:          x.bus.Name,
		Hypothesis:   map[string]string{"polarity": h.polarity},
		Confidence:   i2cConfidence(h, st.evid),
		Bytes:        st.bytes,
		Segments:     st.segs,
		Transactions: st.txns,
		Evidence:     st.evid,
		GroupKey:     "i2c:" + x.bus.Name,
	}
	if h.polarity == "unknown" || len(st.evid) > 0 {
		// retained-ambiguity marker is attached per candidate below
	}
	return cand
}

func i2cConfidence(h i2cHyp, evs []model.Evidence) float64 {
	c := 0.95
	if h.polarity == "unknown" {
		c = 0.5
	}
	penalty := 0.0
	for _, e := range evs {
		switch e.Code {
		case EvI2CMissingEdge:
			penalty += 0.25
		case EvI2CStretch:
			penalty += 0.05
		case EvI2CGap:
			penalty += 0.1
		}
	}
	c -= penalty
	if c < 0.05 {
		c = 0.05
	}
	return c
}

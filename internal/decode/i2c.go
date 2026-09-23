package decode

import (
	"fmt"
	"sort"
)

// i2cHints are single-hypothesis modifications used to build
// alternative candidates from ambiguities found in the base pass.
type i2cHints struct {
	virtualClocks map[int64]bool // insert a sampling edge at t
	flipBits      map[int64]bool // flip the bit sampled at t
	splits        map[int64]bool // force a transaction split at t
}

type i2cAmbiguity struct {
	reason  string
	timePS  int64
	hint    i2cHints
	conf    float64
	paramKV map[string]string
}

type i2cBit struct {
	t       int64
	val     int
	edgeIdx int // material edge index of the sampling clock edge, -1 if virtual
}

func decodeI2C(m *Material, edges []NormEdge, p *Params) ([]Candidate, []Diagnostic, []Event, bool) {
	sclAlias := findChan(m, "i2c", "scl", "clock")
	sdaAlias := findChan(m, "i2c", "sda", "data")
	var diags []Diagnostic
	if sclAlias == "" || sdaAlias == "" {
		diags = append(diags, Diagnostic{Severity: "error", Code: "i2c_channels_missing",
			Message: "i2c bus requires scl/sda channels"})
		return nil, diags, nil, false
	}
	ch := byChannel(edges)
	scl, sda := ch[sclAlias], ch[sdaAlias]
	if len(scl) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "i2c_no_clock",
			Message: "no clock edges on " + sclAlias, Channel: sclAlias})
		return nil, diags, nil, false
	}
	if len(sda) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "i2c_no_data",
			Message: "no data edges on " + sdaAlias, Channel: sdaAlias})
		return nil, diags, nil, false
	}
	cfg := m.channelConfigs()[sclAlias]
	pol := p.polarityFor("i2c", cfg)
	sampleLevel := 1
	if pol == "falling" {
		sampleLevel = 0
	}

	newPass := func(h i2cHints, collect bool) *i2cPass {
		return &i2cPass{scl: scl, sda: sda, sdaLevel: levelAt(sda), sclLevel: levelAt(scl),
			p: p, sampleLevel: sampleLevel, h: h, collect: collect}
	}
	base := newPass(i2cHints{}, true)
	txs := base.run()

	var cands []Candidate
	var events []Event
	cands = append(cands, Candidate{
		ID: candidateID("i2c", pol, i2cHints{}), Bus: "i2c", Confidence: 0.95,
		Reasons: []string{}, Params: map[string]string{"clock_polarity": pol},
		Transactions: txs,
	})
	events = append(events, base.events...)

	// Every ambiguity keeps an extra legal candidate.
	for _, amb := range base.ambs {
		alt := newPass(amb.hint, false)
		altTxs := alt.run()
		cands = append(cands, Candidate{
			ID: candidateID("i2c", pol, amb.hint), Bus: "i2c", Confidence: amb.conf,
			Reasons: []string{amb.reason}, Params: amb.paramKV, Transactions: altTxs,
		})
		events = append(events, Event{Kind: EvCandidateRetained, Code: amb.reason,
			Detail: map[string]any{"bus": "i2c", "time_ps": amb.timePS, "alternatives": 2}})
	}
	return cands, base.diags, events, countBytes(txs) > 0
}

func candidateID(bus, pol string, h i2cHints) string {
	return shortID("cand", fmt.Sprintf("%s|%s|%v|%v|%v", bus, pol,
		sortedKeys(h.virtualClocks), sortedKeys(h.flipBits), sortedKeys(h.splits)))
}

func sortedKeys(m map[int64]bool) []int64 {
	var out []int64
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}

func countBytes(txs []Transaction) int {
	n := 0
	for _, tx := range txs {
		n += len(tx.Bytes)
	}
	return n
}

type i2cPass struct {
	scl, sda    []NormEdge
	sdaLevel    func(int64) int
	sclLevel    func(int64) int
	p           *Params
	h           i2cHints
	sampleLevel int
	collect     bool // collect events/ambiguities (base pass only)

	diags  []Diagnostic
	events []Event
	ambs   []i2cAmbiguity
}

type i2cCond struct {
	t    int64
	kind string // "start" | "stop"
}

func (pass *i2cPass) run() []Transaction {
	// START: SDA falls while SCL high; STOP: SDA rises while SCL high.
	var conds []i2cCond
	for _, e := range pass.sda {
		if pass.sclLevel(e.TimePS) == 1 {
			if e.Level == 0 {
				conds = append(conds, i2cCond{t: e.TimePS, kind: "start"})
			} else {
				conds = append(conds, i2cCond{t: e.TimePS, kind: "stop"})
			}
		}
	}
	// Sampling edges on the configured polarity.
	type samp struct {
		t   int64
		idx int
	}
	var samps []samp
	for _, e := range pass.scl {
		if e.Level == pass.sampleLevel {
			samps = append(samps, samp{t: e.TimePS, idx: e.Index})
		}
	}
	for t := range pass.h.virtualClocks {
		samps = append(samps, samp{t: t, idx: -1})
	}
	sort.Slice(samps, func(a, b int) bool { return samps[a].t < samps[b].t })

	// Median clock period for missing-edge detection.
	median := int64(0)
	{
		var periods []int64
		for i := 1; i < len(pass.scl); i++ {
			if pass.scl[i].Level == pass.sampleLevel && pass.scl[i-1].Level == pass.sampleLevel {
				periods = append(periods, pass.scl[i].TimePS-pass.scl[i-1].TimePS)
			}
		}
		if len(periods) > 0 {
			sort.Slice(periods, func(a, b int) bool { return periods[a] < periods[b] })
			median = periods[len(periods)/2]
		}
	}

	var txs []Transaction
	var cur Transaction
	var bits []i2cBit
	inTx := false
	lastSampT := int64(-1)
	ci := 0 // condition cursor

	flushBytes := func() {
		full := len(bits) / 9
		for i := 0; i < full; i++ {
			grp := bits[i*9 : (i+1)*9]
			val := 0
			for _, b := range grp[:8] {
				val = (val << 1) | b.val
			}
			ack := grp[8].val == 0
			lo, hi := -1, -1
			for _, b := range grp {
				if b.edgeIdx >= 0 && (lo < 0 || b.edgeIdx < lo) {
					lo = b.edgeIdx
				}
				if b.edgeIdx > hi {
					hi = b.edgeIdx
				}
			}
			if lo < 0 {
				lo, hi = 0, 0
			}
			cur.Bytes = append(cur.Bytes, Byte{
				Value: val, Ack: &ack, EdgeStart: lo, EdgeEnd: hi,
				TimeStartPS: grp[0].t, TimeEndPS: grp[8].t, Confidence: 1,
			})
		}
		if rem := len(bits) % 9; rem != 0 {
			t := bits[len(bits)-1].t
			pass.diags = append(pass.diags, Diagnostic{Severity: "warning", Code: "partial_byte",
				Message: fmt.Sprintf("%d dangling bits dropped at byte boundary", rem), TimePS: &t})
		}
		bits = nil
	}

	closeTx := func(gap bool, endT int64) {
		if !inTx {
			return
		}
		flushBytes()
		cur.EndPS = endT
		cur.GapAfter = gap
		if len(cur.Bytes) > 0 {
			cur.Device = fmt.Sprintf("0x%02X", cur.Bytes[0].Value>>1)
			cur.ID = shortID("tx", fmt.Sprintf("i2c|%d|%d|%d", cur.StartPS, cur.EndPS, cur.Bytes[0].Value))
			txs = append(txs, cur)
		}
		inTx = false
		lastSampT = -1
	}

	openTx := func(t int64) {
		cur = Transaction{Decoder: "i2c", StartPS: t}
		inTx = true
		bits = nil
		lastSampT = -1
	}

	// Merge conditions and sampling edges in time order.
	for _, s := range samps {
		for ci < len(conds) && conds[ci].t <= s.t {
			c := conds[ci]
			ci++
			switch c.kind {
			case "start":
				if inTx {
					// Repeated START: keep transaction open, restart byte
					// alignment; an alternative candidate splits here.
					if pass.collect {
						pass.ambs = append(pass.ambs, i2cAmbiguity{
							reason: ReasonRepeatedStart, timePS: c.t, conf: 0.4,
							hint:    i2cHints{splits: map[int64]bool{c.t: true}},
							paramKV: map[string]string{"split_at_repeated_start_ps": fmt.Sprint(c.t)},
						})
					}
					if pass.h.splits[c.t] {
						closeTx(false, c.t)
						openTx(c.t)
					} else {
						flushBytes() // keep completed bytes, drop dangling bits
					}
				} else {
					openTx(c.t)
				}
			case "stop":
				closeTx(false, c.t)
			}
		}
		if !inTx {
			continue
		}
		t := s.t
		// Data gap: never stitch bytes across it.
		if lastSampT >= 0 && t-lastSampT > pass.p.GapThresholdPS {
			closeTx(true, lastSampT)
			if pass.collect {
				pass.events = append(pass.events, Event{Kind: EvGapSplit,
					Detail: map[string]any{"bus": "i2c", "gap_start_ps": lastSampT, "gap_end_ps": t,
						"reason": "data_gap_not_stitched"}})
			}
			pass.diags = append(pass.diags, Diagnostic{Severity: "warning", Code: "resume_without_start",
				Message: "clock resumes after data gap without START; new transaction opened", TimePS: &t})
			openTx(t)
		}
		// Clock stretching: SCL held low beyond threshold.
		if pass.sampleLevel == 1 {
			for i := len(pass.scl) - 1; i >= 0; i-- {
				if pass.scl[i].TimePS >= t {
					continue
				}
				if pass.scl[i].Level != 0 {
					break
				}
				low := t - pass.scl[i].TimePS
				if low > pass.p.StretchThresholdPS && low <= pass.p.GapThresholdPS {
					ft := pass.scl[i].TimePS
					if pass.collect {
						pass.ambs = append(pass.ambs, i2cAmbiguity{
							reason: ReasonClockStretch, timePS: ft, conf: 0.35,
							hint:    i2cHints{splits: map[int64]bool{ft: true}},
							paramKV: map[string]string{"split_at_stretch_ps": fmt.Sprint(ft)},
						})
					}
					if pass.h.splits[ft] {
						closeTx(false, ft)
						openTx(t)
					}
				}
				break
			}
		}
		// Missing clock edge: multiple SDA cells inside one clock period,
		// or a period far longer than the median.
		if pass.collect && lastSampT >= 0 {
			sdaBetween := 0
			var firstSDA int64
			for _, e := range pass.sda {
				if e.TimePS > lastSampT && e.TimePS < t {
					if sdaBetween == 0 {
						firstSDA = e.TimePS
					}
					sdaBetween++
				}
			}
			switch {
			case sdaBetween >= 2:
				pass.ambs = append(pass.ambs, i2cAmbiguity{
					reason: ReasonMissingClockEdge, timePS: firstSDA, conf: 0.55,
					hint:    i2cHints{virtualClocks: map[int64]bool{firstSDA: true}},
					paramKV: map[string]string{"virtual_clock_at_ps": fmt.Sprint(firstSDA)},
				})
			case median > 0 && t-lastSampT > 2*median && t-lastSampT <= pass.p.GapThresholdPS:
				vt := lastSampT + median
				pass.ambs = append(pass.ambs, i2cAmbiguity{
					reason: ReasonMissingClockEdge, timePS: vt, conf: 0.55,
					hint:    i2cHints{virtualClocks: map[int64]bool{vt: true}},
					paramKV: map[string]string{"virtual_clock_at_ps": fmt.Sprint(vt)},
				})
			}
		}
		// Boundary sampling: SDA toggles right at the sampling instant.
		if pass.collect {
			for _, e := range pass.sda {
				d := e.TimePS - t
				if d < 0 {
					d = -d
				}
				if d <= pass.p.BoundaryWindowPS {
					pass.ambs = append(pass.ambs, i2cAmbiguity{
						reason: ReasonBoundarySample, timePS: t, conf: 0.45,
						hint:    i2cHints{flipBits: map[int64]bool{t: true}},
						paramKV: map[string]string{"flip_bit_at_ps": fmt.Sprint(t)},
					})
					break
				}
			}
		}
		lvl := pass.sdaLevel(t)
		if lvl < 0 {
			lvl = 0
		}
		if pass.h.flipBits[t] {
			lvl = 1 - lvl
		}
		bits = append(bits, i2cBit{t: t, val: lvl, edgeIdx: s.idx})
		lastSampT = t
	}
	// Tail conditions after the last sampling edge.
	for ci < len(conds) {
		if conds[ci].kind == "stop" {
			closeTx(false, conds[ci].t)
		}
		ci++
	}
	closeTx(false, lastSampT)
	return txs
}

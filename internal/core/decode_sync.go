package core

import (
	"sort"
)

// decodeSync decodes the custom synchronous channel: DATA sampled on
// the configured clock polarity, grouped MSB-first into bytes. Gaps
// terminate a transaction instead of splicing across the silence.
func decodeSync(cap *Capture, edges []Edge, p Params) ([]Transaction, []Diagnostic) {
	clkChans := cap.ChannelByRole("SYNCCLK")
	dataChans := cap.ChannelByRole("DATA")
	if len(clkChans) == 0 || len(dataChans) == 0 {
		return nil, nil
	}
	clk, data := &clkChans[0], &dataChans[0]
	gapTol := p.GapThresholdUs * 1e-6

	type seg struct {
		bits []int
		ts   []float64
		refs [][]string
	}
	var segs []seg
	var cur *seg
	prev := -1
	lastT := -1.0
	for _, e := range channelEdges(edges, clk.Alias) {
		l := interpretedLevel(clk, e.Level)
		sample := (p.ClockPolarity == "rising" && prev == 0 && l == 1) ||
			(p.ClockPolarity == "falling" && prev == 1 && l == 0)
		prev = l
		if !sample {
			continue
		}
		if cur == nil || (lastT >= 0 && e.Time.Value-lastT > gapTol) {
			if cur != nil {
				segs = append(segs, *cur)
			}
			cur = &seg{}
		}
		bit := levelAt(edges, data, e.Time.Value)
		if bit < 0 {
			bit = 0
		}
		cur.bits = append(cur.bits, bit)
		cur.ts = append(cur.ts, e.Time.Value)
		cur.refs = append(cur.refs, []string{e.ID})
		lastT = e.Time.Value
	}
	if cur != nil {
		segs = append(segs, *cur)
	}

	var txns []Transaction
	for _, s := range segs {
		var bytes []Byte
		n := len(s.bits) / 8
		for i := 0; i < n; i++ {
			v := 0
			var refs []string
			for k := 0; k < 8; k++ {
				v = (v << 1) | s.bits[i*8+k]
				refs = append(refs, s.refs[i*8+k]...)
			}
			bytes = append(bytes, makeByte("", i, v, nil, dedupe(refs), s.ts[i*8], s.ts[i*8+7]))
		}
		if len(bytes) == 0 {
			continue
		}
		t := finishTxn("sync", data.Alias, s.ts[0], s.ts[len(s.ts)-1], bytes, "ok", nil)
		for i := range bytes {
			bytes[i].ID = HashOf("byt", struct {
				T string
				I int
				V int
				E []string
			}{t.ID, bytes[i].Index, bytes[i].Value, bytes[i].EdgeIDs})
		}
		txns = append(txns, t)
	}
	sort.SliceStable(txns, func(i, j int) bool { return txns[i].Start < txns[j].Start })
	return txns, nil
}

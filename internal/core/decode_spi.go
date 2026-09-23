package core

import (
	"fmt"
	"sort"
)

type csInterval struct {
	start, end float64
}

func decodeSPI(cap *Capture, edges []Edge, p Params) ([]Transaction, []Ambiguity, []Diagnostic) {
	clkChans := cap.ChannelByRole("CLK")
	mosiChans := cap.ChannelByRole("MOSI")
	csChans := cap.ChannelByRole("CS")
	if len(clkChans) == 0 || len(csChans) == 0 || len(mosiChans) == 0 {
		return nil, nil, nil
	}
	clk := &clkChans[0]
	mosi := &mosiChans[0]
	var miso *ChannelSpec
	if m := cap.ChannelByRole("MISO"); len(m) > 0 {
		miso = &m[0]
	}

	var txns []Transaction
	var ambs []Ambiguity
	var diags []Diagnostic
	if len(csChans) > 1 {
		diags = append(diags, Diagnostic{
			Code:    DiagMultiDeviceChipSelect,
			Message: fmt.Sprintf("%d chip-select lines decoded as separate devices", len(csChans)),
		})
	}

	clkEdges := channelEdges(edges, clk.Alias)
	boundaryTol := p.BoundaryTolNs * 1e-9

	for _, cs := range csChans {
		cs := cs
		for _, iv := range csIntervals(channelEdges(edges, cs.Alias), &cs) {
			// Clock edges inside this CS assertion.
			var in []Edge
			for _, e := range clkEdges {
				if e.Time.Value >= iv.start && e.Time.Value <= iv.end {
					in = append(in, e)
				}
			}
			if len(in) < 8 {
				continue
			}
			modes := []int{}
			if p.SPIMode != nil {
				modes = append(modes, *p.SPIMode)
			} else {
				modes = []int{0, 1, 2, 3}
				diags = append(diags, Diagnostic{
					Code:    DiagUnknownSPIMode,
					Message: "SPI mode not configured; all four mode hypotheses retained",
					Channel: cs.Alias,
					Time:    iv.start,
					Unit:    "s",
				})
			}
			type hyp struct {
				mode int
				bits []int
				refs [][]string
				ts   []float64
				conf float64
				bnd  bool
			}
			var hyps []hyp
			for _, m := range modes {
				rising := m == 0 || m == 3 // sample edge direction
				var h hyp
				h.mode = m
				h.conf = 1.0
				prev := -1
				for _, e := range in {
					l := interpretedLevel(clk, e.Level)
					isRise := prev == 0 && l == 1
					isFall := prev == 1 && l == 0
					prev = l
					if (rising && !isRise) || (!rising && !isFall) {
						continue
					}
					bit := levelAt(edges, mosi, e.Time.Value)
					if bit < 0 {
						bit = 0
					}
					// Boundary sampling: a MOSI edge within tolerance of
					// the sampling clock makes this bit uncertain.
					if near := edgeNear(edges, mosi.Alias, e.Time.Value, boundaryTol); near != nil {
						h.conf *= 0.5
						h.bnd = true
						bit = interpretedLevel(mosi, near.Level)
						diags = append(diags, Diagnostic{
							Code:    DiagBoundarySample,
							Message: "MOSI transition within sampling tolerance of clock edge",
							Channel: mosi.Alias,
							Time:    e.Time.Value,
							Unit:    "s",
						})
					}
					h.bits = append(h.bits, bit)
					h.ts = append(h.ts, e.Time.Value)
					h.refs = append(h.refs, []string{e.ID})
				}
				if len(h.bits) >= 8 {
					hyps = append(hyps, h)
				}
			}
			if len(hyps) == 0 {
				continue
			}
			// Confidence: unknown modes start equal; boundary-uncertain
			// hypotheses are penalized. Normalize.
			total := 0.0
			for _, h := range hyps {
				total += h.conf
			}
			scope := fmt.Sprintf("spi:%s@%dns", cs.Alias, int64(iv.start*1e9))
			buildTxn := func(h hyp) Transaction {
				var bytes []Byte
				n := len(h.bits) / 8
				for i := 0; i < n; i++ {
					v := 0
					var refs []string
					for k := 0; k < 8; k++ {
						v = (v << 1) | h.bits[i*8+k]
						refs = append(refs, h.refs[i*8+k]...)
					}
					bytes = append(bytes, makeByte("", i, v, nil, dedupe(refs), h.ts[i*8], h.ts[i*8+7]))
				}
				t := finishTxn("spi", cs.Alias, iv.start, iv.end, bytes, "ok", nil)
				for i := range bytes {
					bytes[i].ID = HashOf("byt", struct {
						T string
						I int
						V int
						E []string
					}{t.ID, bytes[i].Index, bytes[i].Value, bytes[i].EdgeIDs})
				}
				return t
			}
			if len(hyps) == 1 && !hyps[0].bnd {
				txns = append(txns, buildTxn(hyps[0]))
				continue
			}
			amb := Ambiguity{Scope: scope}
			if p.SPIMode == nil {
				amb.Reason = DiagUnknownSPIMode
			} else {
				amb.Reason = DiagBoundarySample
			}
			for _, h := range hyps {
				t := buildTxn(h)
				reasons := []string{amb.Reason}
				if h.bnd {
					reasons = append(reasons, DiagBoundarySample)
				}
				c := Candidate{
					Confidence:  h.conf / total,
					Reasons:     reasons,
					Assumptions: map[string]string{"spi_mode": fmt.Sprintf("%d", h.mode)},
					Transaction: t,
				}
				c.ID = HashOf("cand", struct {
					S string
					T Transaction
					M int
				}{scope, t, h.mode})
				amb.Candidates = append(amb.Candidates, c)
			}
			if tiedScores(amb.Candidates) {
				for i := range amb.Candidates {
					amb.Candidates[i].Reasons = append(amb.Candidates[i].Reasons, DiagTiedCandidates)
				}
			}
			amb.ID = HashOf("amb", amb)
			ambs = append(ambs, amb)
		}
	}
	_ = miso
	sort.SliceStable(txns, func(i, j int) bool { return txns[i].Start < txns[j].Start })
	return txns, ambs, diags
}

func csIntervals(csEdges []Edge, cs *ChannelSpec) []csInterval {
	var out []csInterval
	active := false
	start := 0.0
	prev := -1
	for _, e := range csEdges {
		l := interpretedLevel(cs, e.Level)
		if prev == -1 {
			prev = l
			if l == 0 {
				active, start = true, e.Time.Value
			}
			continue
		}
		if prev == 1 && l == 0 {
			active, start = true, e.Time.Value
		} else if prev == 0 && l == 1 && active {
			out = append(out, csInterval{start, e.Time.Value})
			active = false
		}
		prev = l
	}
	if active && len(csEdges) > 0 {
		out = append(out, csInterval{start, csEdges[len(csEdges)-1].Time.Value + 1e-6})
	}
	return out
}

func edgeNear(edges []Edge, alias string, t, tol float64) *Edge {
	for i := range edges {
		e := &edges[i]
		if e.Channel != alias {
			continue
		}
		d := e.Time.Value - t
		if d < 0 {
			d = -d
		}
		if d <= tol {
			return e
		}
	}
	return nil
}

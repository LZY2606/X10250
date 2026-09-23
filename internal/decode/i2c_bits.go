package decode

import (
	"fmt"
	"math/big"

	"edgebench/internal/domain"
)

// readI2CBits consumes SCL cycles in (t0,t1]. Every clock pulse samples SDA on
// the rising edge. Ambiguity and incompleteness become structured state:
// boundary sampling, clock stretching, missing clock edges and data gaps.
func readI2CBits(scl, sda *chTimeline, t0, t1, window, gapLimit, stretchFactor *big.Rat, maxBytes int) ([]bitRead, []domain.Diag) {
	var diags []domain.Diag
	// SCL edges strictly inside the segment (START/STOP edges excluded).
	var se []domain.Edge
	for _, e := range scl.edges {
		if e.T.Cmp(t0) > 0 && e.T.Cmp(t1) <= 0 {
			se = append(se, e)
		}
	}
	// Median clock period estimates the expected cycle (robust to one long low).
	periods := []*big.Rat{}
	for i := 1; i < len(se); i++ {
		periods = append(periods, new(big.Rat).Sub(se[i].T, se[i-1].T))
	}
	median := medianRat(periods)
	stretchLimit := new(big.Rat).Mul(median, stretchFactor)

	var bits []bitRead
	for i := 0; i+1 < len(se); i += 2 {
		fall, rise := se[i], se[i+1]
		if fall.Level != 0 || rise.Level != 1 {
			continue
		}
		low := new(big.Rat).Sub(rise.T, fall.T)
		prev := t0
		if len(bits) > 0 {
			prev = bits[len(bits)-1].clkEnd
		}
		gap := new(big.Rat).Sub(fall.T, prev)

		br := bitRead{sampleT: new(big.Rat).Set(rise.T), clkStart: new(big.Rat).Set(fall.T),
			clkEnd: new(big.Rat).Set(rise.T), sEdge: fall, eEdge: rise, lowPs: low}

		// Data gap: silence far beyond any clock period.
		if len(bits) > 0 && gap.Cmp(gapLimit) > 0 {
			bits = append(bits, bitRead{sampleT: new(big.Rat).Set(fall.T), sEdge: fall, eEdge: fall, value: -1})
			diags = append(diags, domain.Diag{Kind: domain.DiagGap, Severity: "error", Edge: fall.Index,
				AtText: fall.T.RatString(), Detail: fmt.Sprintf("data gap of %sps before %sps; traffic is not spliced", gap.RatString(), fall.T.RatString())})
		}
		// Clock stretching: SCL held low well beyond the normal cycle.
		if median.Sign() > 0 && low.Cmp(stretchLimit) > 0 {
			br.stretch = true
			diags = append(diags, domain.Diag{Kind: domain.DiagStretch, Severity: "info", Edge: fall.Index,
				AtText: fall.T.RatString(), Detail: fmt.Sprintf("SCL low %sps exceeds stretch limit %sps", low.RatString(), stretchLimit.RatString())})
		}
		// Boundary sampling: SDA moves within the sampling window around the
		// rising SCL edge, so the sampled level is not uniquely determined.
		nearest, nearEdge, dist := nearestEdge(sda, rise.T, t0, t1)
		if nearest && dist.Cmp(window) <= 0 {
			br.boundary = true
			alt := nearEdge.Level
			lv, _ := sda.levelAt(rise.T)
			diags = append(diags, domain.Diag{Kind: domain.DiagBoundary, Severity: "warning", Edge: nearEdge.Index,
				AtText: rise.T.RatString(), Detail: fmt.Sprintf("SDA edge %sps from SCL sample (window %sps); level %d vs %d", dist.RatString(), window.RatString(), lv, alt)})
		}
		lv, _ := sda.levelAt(rise.T)
		br.value = lv
		bits = append(bits, br)
		if len(bits) > maxBytes*9+16 {
			return bits, diags
		}
	}
	// Odd trailing SCL edge: clock edge is missing, bit cannot be completed.
	if len(se)%2 == 1 {
		e := se[len(se)-1]
		diags = append(diags, domain.Diag{Kind: domain.DiagMissingEdge, Severity: "error", Edge: e.Index,
			AtText: e.T.RatString(), Detail: "trailing SCL edge without matching edge: incomplete bit, no value fabricated"})
	}
	return bits, diags
}

func nearestEdge(tl *chTimeline, at, lo, hi *big.Rat) (bool, domain.Edge, *big.Rat) {
	best := domain.Edge{}
	found := false
	bestD := (*big.Rat)(nil)
	for _, e := range tl.edges {
		if e.T.Cmp(lo) <= 0 || e.T.Cmp(hi) > 0 {
			continue
		}
		d := new(big.Rat).Sub(e.T, at)
		if d.Sign() < 0 {
			d.Neg(d)
		}
		if !found || d.Cmp(bestD) < 0 {
			found, best, bestD = true, e, d
		}
	}
	return found, best, bestD
}

func medianRat(rs []*big.Rat) *big.Rat {
	if len(rs) == 0 {
		return new(big.Rat)
	}
	out := make([]*big.Rat, len(rs))
	copy(out, rs)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Cmp(out[j]) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return new(big.Rat).Set(out[len(out)/2])
}

func groupBytes(bits []bitRead) []*byteRead {
	var out []*byteRead
	cur := &byteRead{bits: "", start: nil}
	flush := func(complete bool) {
		b := cur
		if b.start == nil {
			cur = &byteRead{bits: ""}
			return
		}
		if !complete {
			b.missing = domain.DiagMissingEdge
		}
		if len(b.bits) == 9 {
			v := 0
			for k := 0; k < 8; k++ {
				v = v<<1 | int(b.bits[k]-'0')
			}
			b.value = v
			b.isAckBit = true
		} else if len(b.bits) > 0 {
			v := 0
			for k := 0; k < len(b.bits); k++ {
				if b.bits[k] == 'x' {
					v = v << 1
				} else {
					v = v<<1 | int(b.bits[k]-'0')
				}
			}
			b.value = v
		}
		out = append(out, b)
		cur = &byteRead{bits: ""}
	}
	for _, br := range bits {
		if br.value < 0 {
			flush(len(cur.bits) == 9 || len(cur.bits) == 0)
			g := &byteRead{bits: "GAP", missing: domain.DiagGap}
			if br.sEdge.Index != 0 || br.sEdge.T != nil {
				g.start = new(big.Rat).Set(br.sampleT)
				g.end = new(big.Rat).Set(br.sampleT)
				g.sEdge, g.eEdge = br.sEdge, br.eEdge
			}
			out = append(out, g)
			cur = &byteRead{bits: ""}
			continue
		}
		if cur.start == nil {
			cur.start = new(big.Rat).Set(br.clkStart)
			cur.sEdge = br.sEdge
		}
		c := byte('0' + br.value)
		if br.boundary {
			c = 'x'
			cur.boundary = true
		}
		if br.stretch {
			cur.stretch = true
		}
		cur.bits += string(c)
		cur.end = new(big.Rat).Set(br.clkEnd)
		cur.eEdge = br.eEdge
		if len(cur.bits) == 9 {
			cur.validFrom = new(big.Rat).Set(cur.start)
			cur.validUntil = new(big.Rat).Set(cur.end)
			flush(true)
		}
	}
	if cur.start != nil {
		flush(false)
	}
	return out
}

func hasGap(by []*byteRead) bool {
	for _, b := range by {
		if b.missing == domain.DiagGap {
			return true
		}
	}
	return false
}

func i2cSummary(by []*byteRead) (string, string) {
	addr, rw := "", ""
	for _, b := range by {
		if b.missing != "" || len(b.bits) < 9 {
			continue
		}
		addr = fmt.Sprintf("0x%02X", b.value>>1)
		if b.bits[7] == '1' {
			rw = "R"
		} else {
			rw = "W"
		}
		break
	}
	note := ""
	if addr != "" {
		note = addr + " " + rw
	}
	return addr, note
}

func byteToRef(runID, txID string, b *byteRead, idx int) domain.ByteRef {
	hexv := ""
	role := "data"
	ack := ""
	if b.missing == domain.DiagGap {
		hexv, role = "GAP", "gap"
	} else if len(b.bits) >= 8 {
		hexv = fmt.Sprintf("%02X", b.value)
		if idx == 0 {
			role = "addr"
		}
		if len(b.bits) == 9 {
			switch b.bits[8] {
			case '0':
				ack = "ack"
			case '1':
				ack = "nack"
			default:
				ack = "?"
			}
		}
	} else {
		hexv = "?"
		role = "payload"
	}
	id := byteID(runID, txID, idx, b.bits, b.missing)
	ref := domain.ByteRef{ID: id, RunID: runID, TxID: txID, Index: idx, Hex: hexv, Bits: b.bits,
		Role: role, Ack: ack, EdgeStart: b.sEdge.Index, EdgeEnd: b.eEdge.Index,
		StartPs: ratStr(b.start), EndPs: ratStr(b.end)}
	if b.samplePoint() != nil {
		ref.SamplePs = b.samplePoint().RatString()
	}
	ref.ValidFrom, ref.ValidUntil = ratStr(b.validFrom), ratStr(b.validUntil)
	if b.missing != "" && b.missing != domain.DiagGap {
		ref.Invalid = b.missing
	}
	return ref
}

func (b *byteRead) samplePoint() *big.Rat { return b.end }

func gapFragments(runID, txID string, by []*byteRead) []domain.Tx {
	var frags []domain.Tx
	var cur *domain.Tx
	idx := 0
	for _, b := range by {
		if b.missing == domain.DiagGap {
			cur = nil
			idx = 0
			continue
		}
		if cur == nil {
			fid := "fg-" + stableID(runID, txID, b.start.RatString())
			f := domain.Tx{ID: fid, Kind: domain.Fragment, Bus: domain.BusI2C, StartPs: ratStr(b.start),
				EndPs: ratStr(b.end), Invalid: "fragment after data gap; not spliced across gap"}
			cur = &f
			frags = append(frags, *cur)
		}
		ref := byteToRef(runID, cur.ID, b, idx)
		frags[len(frags)-1].Bytes = append(frags[len(frags)-1].Bytes, ref)
		frags[len(frags)-1].EndPs = ratStr(b.end)
		idx++
	}
	return frags
}

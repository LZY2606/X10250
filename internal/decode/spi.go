package decode

import (
	"fmt"

	"edgedeck/internal/model"
)

// decodeSPI decodes per-chip-select frames. Unknown SPI mode keeps multiple
// legal candidates; overlapping chip selects are flagged, never merged;
// gaps inside a frame close the transaction without stitching.
func decodeSPI(cap *model.Capture, p *model.Params, d *model.Decode) []model.Transaction {
	sclk, ok1 := roleOf(cap, "sclk")
	mosi, ok2 := roleOf(cap, "mosi")
	css := csChannels(cap)
	if !ok1 || !ok2 || len(css) == 0 {
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: "MISSING_CHANNEL", Severity: "error",
			Message: "spi decode requires roles sclk, mosi and at least one cs",
		})
		return nil
	}

	modes := []string{p.SPIMode}
	unknown := p.SPIMode == "" || p.SPIMode == "unknown"
	if unknown {
		modes = []string{"0", "3"} // keep all legal sampling-polarity candidates
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: model.ReasonSPIModeUnknown, Severity: "warn",
			Message: "SPI mode not determined; keeping candidates for mode 0 and mode 3",
		})
	}

	type frame struct {
		cs         string
		start, end int64
	}
	var frames []frame
	for _, cs := range css {
		active := false
		start := int64(0)
		for _, e := range cap.Edges {
			if e.Channel != cs {
				continue
			}
			lvl := e.Level
			if inverted(cap, cs) {
				lvl = 1 - lvl
			}
			if lvl == 0 && !active {
				active, start = true, e.TimeNS
			} else if lvl == 1 && active {
				active = false
				frames = append(frames, frame{cs, start, e.TimeNS})
			}
		}
		if active {
			frames = append(frames, frame{cs, start, cap.Edges[len(cap.Edges)-1].TimeNS})
		}
	}
	// overlap detection across devices
	for i := 0; i < len(frames); i++ {
		for j := i + 1; j < len(frames); j++ {
			if frames[i].cs != frames[j].cs && frames[i].start < frames[j].end && frames[j].start < frames[i].end {
				d.Diagnostics = append(d.Diagnostics, model.Diag{
					Code: model.ReasonMultiCS, Severity: "warn", TimeNS: frames[j].start,
					Message: fmt.Sprintf("chip selects %s and %s active simultaneously; transactions kept separate", frames[i].cs, frames[j].cs),
				})
			}
		}
	}

	var txs []model.Transaction
	for _, f := range frames {
		// split frame at data gaps — never stitch across them
		var segs [][2]int64
		segStart := f.start
		last := f.start
		for _, e := range cap.Edges {
			if e.TimeNS < f.start || e.TimeNS > f.end || (e.Channel != sclk && e.Channel != mosi) {
				continue
			}
			if p.GapNS > 0 && e.TimeNS-last > p.GapNS {
				d.Diagnostics = append(d.Diagnostics, model.Diag{
					Code: model.ReasonGapNoStitch, Severity: "warn", TimeNS: last,
					Message: fmt.Sprintf("data gap of %d ns inside CS %s; frame closed, not stitched", e.TimeNS-last, f.cs),
				})
				segs = append(segs, [2]int64{segStart, last})
				segStart = e.TimeNS
			}
			last = e.TimeNS
		}
		segs = append(segs, [2]int64{segStart, f.end})

		for _, seg := range segs {
			for _, m := range modes {
				tx, ok := spiSegment(cap, p, d, f.cs, seg[0], seg[1], sclk, mosi, m, unknown)
				if !ok {
					return txs
				}
				if tx != nil {
					txs = append(txs, *tx)
				}
			}
		}
	}
	return txs
}

func spiSegment(cap *model.Capture, p *model.Params, d *model.Decode, cs string, start, end int64, sclk, mosi, mode string, unknown bool) (*model.Transaction, bool) {
	// sampling edge: modes 0/1 sample on first edge polarity; we support
	// cpha=0 sampling: mode 0 -> rising, mode 3 -> falling. Probe clock
	// polarity config flips the effective edge.
	sampleRising := mode == "0" || mode == "1"
	if cap.ClockPolarity == "inverted" {
		sampleRising = !sampleRising
	}
	var bits []bitCell
	for i, e := range cap.Edges {
		if e.Channel != sclk || e.TimeNS < start || e.TimeNS > end {
			continue
		}
		lvl := e.Level
		if inverted(cap, sclk) {
			lvl = 1 - lvl
		}
		rising := lvl == 1
		if rising != sampleRising {
			continue
		}
		v := levelAt(cap, mosi, e.TimeNS, 0)
		amb := false
		for _, me := range cap.Edges {
			if me.Channel == mosi && abs(me.TimeNS-e.TimeNS) <= p.BoundaryNS {
				amb = true
				break
			}
		}
		bits = append(bits, bitCell{val: v, amb: amb, edgeRef: i, timeNS: e.TimeNS})
	}
	if len(bits) == 0 {
		return nil, true
	}
	partial := len(bits)%8 != 0

	mkCand := func(bs []bitCell, label string, rs []string) ([]model.Candidate, bool) {
		combos, ok := expandBits(bs, p.MaxCandidates)
		if !ok {
			budgetDiag(d, label, p.MaxCandidates)
			return nil, false
		}
		var out []model.Candidate
		for ci, combo := range combos {
			amb := 0
			for _, b := range bs {
				if b.amb {
					amb++
				}
			}
			cbytes := packBytes(combo, bs, 8)
			if partial {
				rem := len(combo) % 8
				if rem > 0 {
					base := len(combo) - rem
					v := 0
					refs := []int{}
					for j := 0; j < rem; j++ {
						v = v<<1 | combo[base+j]
						refs = append(refs, bs[base+j].edgeRef)
					}
					cbytes = append(cbytes, model.Byte{
						Value: v, EdgeRefs: refs, Flags: []string{"PARTIAL", model.ReasonClockMissingEdge},
						StartNS: bs[base].timeNS, EndNS: bs[len(bs)-1].timeNS,
					})
				}
				rs = append(rs, model.ReasonClockMissingEdge)
			}
			if amb > 0 {
				rs = append(rs, model.ReasonBoundarySample)
			}
			lbl := label
			if ci > 0 {
				lbl = fmt.Sprintf("%s/alt%d", label, ci)
			}
			out = append(out, model.Candidate{
				Label: lbl, Confidence: confFor(rs, amb), Reasons: rs, Bytes: cbytes, CS: cs,
				Detail: fmt.Sprintf("mode %s, probe clock polarity %s", mode, cap.ClockPolarity),
			})
		}
		return out, true
	}

	reasons := []string{}
	if unknown {
		reasons = append(reasons, model.ReasonSPIModeUnknown)
	}
	cands, ok := mkCand(bits, "mode"+mode, reasons)
	if !ok {
		return nil, false
	}
	if p.MaxCandidates > 0 && len(cands) > p.MaxCandidates {
		budgetDiag(d, "spi segment", p.MaxCandidates)
		return nil, false
	}
	tx := &model.Transaction{Kind: "spi", StartNS: start, EndNS: end, Candidates: cands}
	tx.ID = model.HashID("tx", d.ID, cs, start, end, mode)
	for i := range tx.Candidates {
		tx.Candidates[i].ID = model.HashID("cand", tx.ID, tx.Candidates[i].Label, tx.Candidates[i].Bytes)
	}
	model.SortCandidates(tx.Candidates)
	return tx, true
}

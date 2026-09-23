package decode

import (
	"fmt"

	"edgedeck/internal/model"
)

// decodeI2C runs the I2C state machine over the merged edge stream.
// Handles: repeated START, clock stretching, data gaps (never stitched),
// missing clock edges and boundary sampling — each producing structured
// diagnostics and, where the evidence is ambiguous, multiple candidates.
func decodeI2C(cap *model.Capture, p *model.Params, d *model.Decode) []model.Transaction {
	sda, ok1 := roleOf(cap, "sda")
	scl, ok2 := roleOf(cap, "scl")
	if !ok1 || !ok2 {
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: "MISSING_CHANNEL", Severity: "error",
			Message: "i2c decode requires channels with role sda and scl",
		})
		return nil
	}

	type region struct {
		startNS, endNS int64
		bits           []bitCell
		reasons        []string
		stretchAt      int64 // first stretch point, 0 if none
		stretchBit     int
		partial        bool
	}
	var regions []region
	var cur *region

	sdaLvl, sclLvl := 1, 1
	lastEdge := int64(-1 << 60)
	sclLastFall := int64(-1 << 60)

	finish := func(endNS int64) {
		if cur == nil {
			return
		}
		cur.endNS = endNS
		if len(cur.bits)%9 != 0 {
			cur.partial = true
		}
		regions = append(regions, *cur)
		cur = nil
	}

	for i, e := range cap.Edges {
		t := e.TimeNS
		lvl := e.Level
		if inverted(cap, e.Channel) {
			lvl = 1 - lvl
		}
		// data gap: never stitch across a gap
		if cur != nil && p.GapNS > 0 && t-lastEdge > p.GapNS {
			d.Diagnostics = append(d.Diagnostics, model.Diag{
				Code: model.ReasonGapNoStitch, Severity: "warn", TimeNS: lastEdge,
				Message: fmt.Sprintf("data gap of %d ns exceeds %d ns; transaction closed, bytes not stitched", t-lastEdge, p.GapNS),
			})
			finish(lastEdge)
		}
		lastEdge = t

		switch e.Channel {
		case sda:
			if sclLvl == 1 { // sda moved while scl high: START/STOP class
				if lvl == 0 { // falling: START or repeated START
					if cur != nil {
						d.Diagnostics = append(d.Diagnostics, model.Diag{
							Code: model.ReasonRepeatedStart, Severity: "warn", TimeNS: t,
							Message: "repeated START: new address phase without STOP",
						})
						cur.reasons = append(cur.reasons, model.ReasonRepeatedStart)
						cur.bits = nil // new address phase restarts byte framing
					} else {
						cur = &region{startNS: t}
					}
				} else { // rising: STOP
					finish(t)
				}
			}
			sdaLvl = lvl
		case scl:
			if lvl == 1 { // rising: sample point
				if cur != nil {
					if p.StretchNS > 0 && t-sclLastFall > p.StretchNS {
						d.Diagnostics = append(d.Diagnostics, model.Diag{
							Code: model.ReasonClockStretch, Severity: "warn", TimeNS: sclLastFall,
							Message: fmt.Sprintf("clock stretching: SCL low for %d ns", t-sclLastFall),
						})
						cur.reasons = append(cur.reasons, model.ReasonClockStretch)
						if cur.stretchAt == 0 {
							cur.stretchAt = t
							cur.stretchBit = len(cur.bits)
						}
					}
					amb := false
					for _, se := range cap.Edges {
						if se.Channel == sda && abs(se.TimeNS-t) <= p.BoundaryNS {
							amb = true
							break
						}
					}
					cur.bits = append(cur.bits, bitCell{val: sdaLvl, amb: amb, edgeRef: i, timeNS: t})
				}
			} else {
				sclLastFall = t
			}
			sclLvl = lvl
		}
	}
	finish(lastEdge)

	var txs []model.Transaction
	for _, r := range regions {
		if len(r.bits) == 0 {
			continue
		}
		tx := model.Transaction{Kind: "i2c", StartNS: r.startNS, EndNS: r.endNS}
		cands, ok := i2cCandidates(cap, p, d, r.bits, r.reasons, r.stretchBit, r.partial)
		if !ok {
			return txs // budget exhausted; status already set
		}
		tx.Candidates = cands
		tx.ID = model.HashID("tx", d.ID, tx.StartNS, tx.EndNS, tx.Kind)
		for i := range tx.Candidates {
			tx.Candidates[i].ID = model.HashID("cand", tx.ID, tx.Candidates[i].Label, tx.Candidates[i].Bytes)
		}
		model.SortCandidates(tx.Candidates)
		txs = append(txs, tx)
	}
	return txs
}

func i2cCandidates(cap *model.Capture, p *model.Params, d *model.Decode, bits []bitCell, reasons []string, stretchBit int, partial bool) ([]model.Candidate, bool) {
	full := len(bits)
	usable := full
	if partial {
		usable = full - full%9 // drop incomplete byte frame for the base stream
	}
	mk := func(bs []bitCell, label string, rs []string, dropPartial bool) ([]model.Candidate, bool) {
		use := bs
		if dropPartial && partial {
			use = bs[:usable]
		}
		combos, ok := expandBits(use, p.MaxCandidates)
		if !ok {
			budgetDiag(d, label, p.MaxCandidates)
			return nil, false
		}
		var out []model.Candidate
		for ci, combo := range combos {
			amb := 0
			for _, b := range use {
				if b.amb {
					amb++
				}
			}
			cbytes := []model.Byte{}
			for i := 0; i+9 <= len(combo); i += 9 {
				v := 0
				refs := []int{}
				for j := 0; j < 8; j++ {
					v = v<<1 | combo[i+j]
					refs = append(refs, use[i+j].edgeRef)
				}
				ack := "NACK"
				if combo[i+8] == 0 {
					ack = "ACK"
				}
				refs = append(refs, use[i+8].edgeRef)
				cbytes = append(cbytes, model.Byte{
					Value: v, Ack: ack, EdgeRefs: refs,
					StartNS: use[i].timeNS, EndNS: use[i+8].timeNS,
				})
			}
			if partial && !dropPartial {
				// keep the partial frame visible, flagged, never silently dropped
				rem := combo[len(combo)-len(combo)%9:]
				if len(rem) > 0 {
					v := 0
					refs := []int{}
					base := len(combo) - len(combo)%9
					for j, b := range rem {
						v = v<<1 | b
						refs = append(refs, use[base+j].edgeRef)
					}
					cbytes = append(cbytes, model.Byte{
						Value: v, EdgeRefs: refs, Flags: []string{"PARTIAL", model.ReasonClockMissingEdge},
						StartNS: use[base].timeNS, EndNS: use[len(use)-1].timeNS,
					})
				}
			}
			rs2 := append([]string{}, rs...)
			if partial && !dropPartial {
				rs2 = append(rs2, model.ReasonClockMissingEdge)
			}
			if amb > 0 {
				rs2 = append(rs2, model.ReasonBoundarySample)
			}
			lbl := label
			if ci > 0 {
				lbl = fmt.Sprintf("%s/alt%d", label, ci)
			}
			out = append(out, model.Candidate{
				Label: lbl, Confidence: confFor(rs2, amb), Reasons: rs2, Bytes: cbytes,
			})
		}
		return out, true
	}

	var cands []model.Candidate
	// primary: full stream including partial frame (keeps missing-edge evidence)
	cs, ok := mk(bits, "full", reasons, false)
	if !ok {
		return nil, false
	}
	cands = append(cands, cs...)
	// alternative: drop the partial frame
	if partial {
		cs, ok := mk(bits, "no-partial", reasons, true)
		if !ok {
			return nil, false
		}
		for i := range cs {
			cs[i].Reasons = append(cs[i].Reasons, model.ReasonClockMissingEdge)
			cs[i].Confidence = confFor(cs[i].Reasons, 0)
		}
		cands = append(cands, cs...)
	}
	// alternative: clock-stretch split — bytes before the stretch point only
	if stretchBit > 0 {
		head := bits[:stretchBit-stretchBit%9]
		if len(head) >= 9 {
			cs, ok := mk(head, "stretch-split", append(append([]string{}, reasons...), model.ReasonClockStretch), false)
			if !ok {
				return nil, false
			}
			cands = append(cands, cs...)
		}
	}
	if p.MaxCandidates > 0 && len(cands) > p.MaxCandidates {
		budgetDiag(d, "i2c transaction", p.MaxCandidates)
		return nil, false
	}
	return cands, true
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

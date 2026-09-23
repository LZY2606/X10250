package decode

import (
	"edgedeck/internal/model"
)

// decodeSync decodes a custom synchronous clock+data protocol: frames are
// maximal runs of clock edges spaced within GapNS; SyncBits per word.
func decodeSync(cap *model.Capture, p *model.Params, d *model.Decode) []model.Transaction {
	clk, ok1 := roleOf(cap, "clk")
	data, ok2 := roleOf(cap, "data")
	if !ok1 || !ok2 {
		d.Diagnostics = append(d.Diagnostics, model.Diag{
			Code: "MISSING_CHANNEL", Severity: "error",
			Message: "sync decode requires channels with role clk and data",
		})
		return nil
	}
	width := p.SyncBits
	if width <= 0 {
		width = 8
	}

	type run struct {
		start, end int64
		bits       []bitCell
	}
	var runs []run
	var cur *run
	lastClk := int64(-1 << 60)
	for i, e := range cap.Edges {
		if e.Channel != clk {
			continue
		}
		lvl := e.Level
		if inverted(cap, clk) {
			lvl = 1 - lvl
		}
		if lvl != 1 { // sample on rising clock
			continue
		}
		if cur == nil || (p.GapNS > 0 && e.TimeNS-lastClk > p.GapNS) {
			if cur != nil {
				d.Diagnostics = append(d.Diagnostics, model.Diag{
					Code: model.ReasonGapNoStitch, Severity: "warn", TimeNS: lastClk,
					Message: "clock gap closes sync frame; words not stitched",
				})
				runs = append(runs, *cur)
			}
			cur = &run{start: e.TimeNS}
		}
		lastClk = e.TimeNS
		cur.end = e.TimeNS
		v := levelAt(cap, data, e.TimeNS, 0)
		amb := false
		for _, de := range cap.Edges {
			if de.Channel == data && abs(de.TimeNS-e.TimeNS) <= p.BoundaryNS {
				amb = true
				break
			}
		}
		cur.bits = append(cur.bits, bitCell{val: v, amb: amb, edgeRef: i, timeNS: e.TimeNS})
	}
	if cur != nil {
		runs = append(runs, *cur)
	}

	var txs []model.Transaction
	for _, r := range runs {
		if len(r.bits) == 0 {
			continue
		}
		partial := len(r.bits)%width != 0
		combos, ok := expandBits(r.bits, p.MaxCandidates)
		if !ok {
			budgetDiag(d, "sync frame", p.MaxCandidates)
			return txs
		}
		var cands []model.Candidate
		for ci, combo := range combos {
			amb := 0
			for _, b := range r.bits {
				if b.amb {
					amb++
				}
			}
			cbytes := packBytes(combo, r.bits, width)
			rs := []string{}
			if partial {
				rs = append(rs, model.ReasonClockMissingEdge)
				rem := len(combo) % width
				base := len(combo) - rem
				v := 0
				refs := []int{}
				for j := 0; j < rem; j++ {
					v = v<<1 | combo[base+j]
					refs = append(refs, r.bits[base+j].edgeRef)
				}
				cbytes = append(cbytes, model.Byte{
					Value: v, EdgeRefs: refs, Flags: []string{"PARTIAL", model.ReasonClockMissingEdge},
					StartNS: r.bits[base].timeNS, EndNS: r.bits[len(r.bits)-1].timeNS,
				})
			}
			if amb > 0 {
				rs = append(rs, model.ReasonBoundarySample)
			}
			lbl := "sync"
			if ci > 0 {
				lbl = "sync/alt" + itoa(ci)
			}
			cands = append(cands, model.Candidate{
				Label: lbl, Confidence: confFor(rs, amb), Reasons: rs, Bytes: cbytes,
			})
		}
		if p.MaxCandidates > 0 && len(cands) > p.MaxCandidates {
			budgetDiag(d, "sync frame", p.MaxCandidates)
			return txs
		}
		tx := model.Transaction{Kind: "sync", StartNS: r.start, EndNS: r.end, Candidates: cands}
		tx.ID = model.HashID("tx", d.ID, r.start, r.end, "sync")
		for i := range tx.Candidates {
			tx.Candidates[i].ID = model.HashID("cand", tx.ID, tx.Candidates[i].Label, tx.Candidates[i].Bytes)
		}
		model.SortCandidates(tx.Candidates)
		txs = append(txs, tx)
	}
	return txs
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

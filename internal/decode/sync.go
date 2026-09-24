package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// decodeSync handles a generic clock/data bus. Clock polarity selects which
// edge samples data; ambiguous configuration keeps both edge candidates.
func decodeSync(res *Result, sigs map[string]*Sig, p ParamsView, budget Budget) error {
	clk, data := sigs["clk"], sigs["data"]
	period := clockPeriod(clk)
	gap := period * 8
	if q := p.GapThreshold(); q.OK && q.Value > 0 {
		gap = q.Value
	}
	msbFirst := p.BitOrder() != "lsb"
	wants := []struct {
		edge  int
		key   string
		label string
	}{
		{1, "sync:rising", "Sync sample-on-rising"},
		{0, "sync:falling", "Sync sample-on-falling"},
	}
	if p.ClockPolarity() == "rising" {
		wants = wants[:1]
	}
	if p.ClockPolarity() == "falling" {
		wants = wants[1:]
	}
	for _, w := range wants {
		if err := budget.Use(len(clk.Times)); err != nil {
			return err
		}
		var bits []Bit
		var evs []Evidence
		last := -1.0
		for i, t := range clk.Times {
			if clk.Levels[i] != w.edge {
				continue
			}
			if last >= 0 && period > 0 && t-last > gap {
				evs = append(evs, Evidence{Code: "data-gap", StartSec: last, EndSec: t,
					Detail: fmt.Sprintf("clock gap %s; no splice", fmtSec(t-last))})
				bits = append(bits, Bit{Note: "GAP", Start: last, End: t})
			}
			bits = append(bits, Bit{Index: len(bits), Value: data.LevelAt(t), Start: t,
				End: nextSCLK(clk, i), SampleAt: t, ClockEdge: clk.Edges[i].ID,
				DataEdgeIDs: data.EdgesBetween(t-period, t+period/2)})
			last = t
		}
		txns := bitsToSyncTxns(bits, msbFirst, evs, period)
		res.Candidates = append(res.Candidates, Candidate{
			Key: w.key, Label: w.label, Confidence: 0.6, Transactions: txns,
		})
	}
	return nil
}

func bitsToSyncTxns(bits []Bit, msbFirst bool, evs []Evidence, period float64) []Transaction {
	var txns []Transaction
	var group []Bit
	var gEvs []Evidence
	start := -1.0
	end := 0.0
	flush := func() {
		if len(group) == 0 && len(gEvs) == 0 {
			return
		}
		var byts []Byte
		for off := 0; off+8 <= len(group); off += 8 {
			val := 0
			chunk := group[off : off+8]
			for b := 0; b < 8; b++ {
				src := b
				if !msbFirst {
					src = 7 - b
				}
				bit := chunk[src]
				bit.Index = b
				if bit.Value == 1 {
					val |= 1 << uint(7-b)
				}
			}
			byts = append(byts, Byte{Index: off / 8, Value: val, Bits: chunk,
				Start: chunk[0].Start, End: chunk[7].End, Ack: "NONE"})
		}
		inv := ""
		ge := append([]Evidence{}, gEvs...)
		if len(group)%8 != 0 {
			ge = append(ge, Evidence{Code: "missing-clock-edge", StartSec: group[len(group)-1].Start,
				EndSec: end, Detail: "trailing clocks do not complete a byte"})
			inv = "incomplete byte at gap"
		}
		txns = append(txns, Transaction{
			Kind: model.KindSync, Index: len(txns), Start: start, End: end,
			Bytes: byts, Mode: "sync", ValidFrom: start, ValidTo: end,
			Invalid: inv, Evidence: ge,
		})
		group = nil
		gEvs = nil
		start = -1
	}
	for _, b := range bits {
		if b.Note == "GAP" {
			flush()
			gEvs = append(gEvs, Evidence{Code: "data-gap", StartSec: b.Start, EndSec: b.End, Detail: "gap"})
			end = b.End
			continue
		}
		if start < 0 {
			start = b.Start
		}
		group = append(group, b)
		end = b.End
	}
	flush()
	return txns
}

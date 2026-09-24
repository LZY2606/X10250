package decode

import (
	"fmt"

	"edgebench/internal/edge"
)

// decodeI2C handles START/STOP/repeated-START, clock stretching, missing
// clock edges (partial byte -> competing candidates) and data gaps (never
// stitched across).
func decodeI2C(cap *edge.Capture, p Params) Result {
	res := Result{Status: StatusOK}
	scl, sda := p.Roles["scl"], p.Roles["sda"]
	if scl == "" || sda == "" {
		res.Status = StatusUnsolvable
		res.Diagnostics = append(res.Diagnostics, edge.Diagnostic{Pos: "/roles", Code: "missing-role", Message: "i2c needs scl and sda roles"})
		return res
	}
	evs := stream(cap, scl, sda)
	if len(evs) == 0 {
		res.Status = StatusUnsolvable
		res.Diagnostics = append(res.Diagnostics, edge.Diagnostic{Pos: "/edges", Code: "no-edges", Message: "no edges on scl/sda"})
		return res
	}
	sampleOnRising := p.ClockPolarity != "falling"

	type txnState struct {
		tx      Transaction
		bits    []int
		bitRefs [][]string
		bitT    []int64
		bytes   []Byte
	}
	var txns []Transaction
	var cur *txnState
	sclLvl, sdaLvl := -1, -1
	sclFellAt := int64(-1)
	lastT := int64(-1)
	bud := &budget{max: p.MaxCandidates}
	sawStart := false

	closeTxn := func(endT int64, gap bool) {
		if cur == nil {
			return
		}
		cur.tx.EndNs = endT
		cur.tx.GapAfter = gap
		rem := len(cur.bits) % 9
		base := Candidate{Label: "strict", Bytes: append([]Byte{}, cur.bytes...), Confidence: 1}
		if rem == 0 {
			cur.tx.Candidates = []Candidate{base}
		} else {
			// missing clock edges: partial byte. Keep both legal readings.
			if !bud.take() || !bud.take() {
				res.Status = StatusBudgetExhausted
				res.Diagnostics = append(res.Diagnostics, edge.Diagnostic{
					Pos: fmt.Sprintf("/transactions/%d", len(txns)), Code: "budget-candidates",
					Message: "candidate budget exhausted resolving partial byte"})
				return
			}
			drop := base
			drop.Label = "drop-partial"
			drop.Confidence = clampConf(1 - 0.25)
			drop.Notes = []string{fmt.Sprintf("dropped %d dangling bit(s): clock edges missing before stop/gap", rem)}
			pad := Candidate{Label: "pad-partial", Bytes: append([]Byte{}, cur.bytes...), Confidence: clampConf(1 - 0.35),
				Notes: []string{fmt.Sprintf("padded %d missing bit(s) with 0: clock edges missing", 9-rem)}}
			v := 0
			for i := 0; i < rem && i < 8; i++ {
				v = (v << 1) | cur.bits[len(cur.bits)-rem+i]
			}
			v <<= 8 - rem
			var refs []string
			for _, r := range cur.bitRefs[len(cur.bitRefs)-rem:] {
				refs = append(refs, r...)
			}
			pad.Bytes = append(pad.Bytes, Byte{Value: v, TNs: cur.bitT[len(cur.bitT)-rem], EdgeIDs: refs})
			cur.tx.Candidates = []Candidate{drop, pad}
		}
		txns = append(txns, cur.tx)
		cur = nil
	}

	for _, e := range evs {
		lvl := effLevel(cap, e.Channel, e.Level)
		if cur != nil && lastT >= 0 && e.TimeNs-lastT > p.GapThresholdNs {
			closeTxn(lastT, true)
		}
		lastT = e.TimeNs
		switch e.Channel {
		case sda:
			if sclLvl == 1 {
				if sdaLvl == 1 && lvl == 0 {
					// START or repeated START
					if cur == nil {
						cur = &txnState{tx: Transaction{Protocol: "i2c", StartNs: e.TimeNs,
							Markers: []Marker{{Kind: "start", TNs: e.TimeNs}}}}
						sawStart = true
					} else {
						cur.tx.Markers = append(cur.tx.Markers, Marker{Kind: "repeated-start", TNs: e.TimeNs})
					}
				} else if sdaLvl == 0 && lvl == 1 && cur != nil {
					cur.tx.Markers = append(cur.tx.Markers, Marker{Kind: "stop", TNs: e.TimeNs})
					closeTxn(e.TimeNs, false)
				}
			}
			sdaLvl = lvl
		case scl:
			if sclLvl == 0 && lvl == 1 && sclFellAt >= 0 && e.TimeNs-sclFellAt > p.StretchThresholdNs && cur != nil {
				cur.tx.Markers = append(cur.tx.Markers, Marker{Kind: "stretch", TNs: sclFellAt})
			}
			if lvl == 0 {
				sclFellAt = e.TimeNs
			}
			isSample := (sampleOnRising && sclLvl == 0 && lvl == 1) || (!sampleOnRising && sclLvl == 1 && lvl == 0)
			if isSample && cur != nil && sdaLvl >= 0 {
				cur.bits = append(cur.bits, sdaLvl)
				cur.bitRefs = append(cur.bitRefs, []string{e.ID})
				cur.bitT = append(cur.bitT, e.TimeNs)
				if len(cur.bits)%9 == 0 {
					v := 0
					for _, b := range cur.bits[len(cur.bits)-8:] {
						v = (v << 1) | b
					}
					ack := "nack"
					if cur.bits[len(cur.bits)-1] == 0 {
						ack = "ack"
					}
					var refs []string
					for _, r := range cur.bitRefs[len(cur.bitRefs)-9:] {
						refs = append(refs, r...)
					}
					cur.bytes = append(cur.bytes, Byte{Value: v, Ack: ack, TNs: cur.bitT[len(cur.bitT)-9], EdgeIDs: refs})
					if len(cur.bytes) == 1 && len(cur.tx.Markers) > 0 && cur.tx.Markers[len(cur.tx.Markers)-1].Kind != "stop" {
						cur.tx.Device = fmt.Sprintf("0x%02x", v>>1)
					}
				}
			}
			sclLvl = lvl
		}
	}
	if cur != nil {
		closeTxn(lastT, true) // capture ended mid-transaction: treat as gap
	}
	if res.Status == StatusBudgetExhausted {
		return res
	}
	if !sawStart {
		res.Status = StatusUnsolvable
		res.Diagnostics = append(res.Diagnostics, edge.Diagnostic{Pos: "/edges", Code: "no-start",
			Message: "no START condition found; cannot frame any transaction"})
		return res
	}
	for i := range txns {
		txns[i].ID = fmt.Sprintf("i2c-txn-%03d", i+1)
		finishCandidates(txns[i].Candidates)
	}
	res.Transactions = txns
	return res
}

package decoder

import (
	"fmt"

	"edgebench/internal/domain"
)

type bit struct {
	t      int64
	v      int
	ambig  bool
	edgeID string
}

func (e *engine) decodeI2C(bus domain.Bus) error {
	sdaCh, sclCh := bus.Channels["sda"], bus.Channels["scl"]
	p := e.in.Params.I2C
	type cond struct {
		t    int64
		stop bool
	}
	var conds []cond
	for _, ed := range e.edgesBy[sdaCh] {
		if e.levelAt(sclCh, ed.TNs, true) != 1 {
			continue
		}
		conds = append(conds, cond{t: ed.TNs, stop: ed.Level == 1})
	}
	type seg struct {
		start, end int64
		hasStop    bool
	}
	var segs []seg
	open := false
	var cur seg
	for _, c := range conds {
		if !c.stop {
			if open {
				cur.end = c.t
				segs = append(segs, cur)
			}
			cur = seg{start: c.t}
			open = true
		} else if open {
			cur.end = c.t
			cur.hasStop = true
			segs = append(segs, cur)
			open = false
		}
	}
	if open {
		cur.end = -1
		segs = append(segs, cur)
	}
	sclEdges := e.edgesBy[sclCh]
	var prevTxnID string
	var prevStopT int64
	for si, s := range segs {
		end := s.end
		if end < 0 {
			if len(sclEdges) > 0 {
				end = sclEdges[len(sclEdges)-1].TNs
			} else {
				end = s.start
			}
		}
		txnID := "txn-" + domain.Fingerprint(bus.ID, bus.Label, fmt.Sprint(s.start), fmt.Sprint(end), fmt.Sprint(si))
		if si > 0 && !segs[si-1].hasStop {
			e.event("repeated_start", txnID, map[string]any{
				"bus": bus.ID, "t_ns": s.start,
				"left_txn": prevTxnID, "right_txn": txnID,
				"note": "START while transaction open; previous segment closed without STOP",
			})
		}
		if prevTxnID != "" && s.start-prevStopT > p.MaxGapNs {
			e.event("data_gap", txnID, map[string]any{
				"bus": bus.ID, "left_txn": prevTxnID, "right_txn": txnID,
				"gap_ns": s.start - prevStopT, "spliced": false,
				"note": "data gap exceeds max_gap; transactions kept separate",
			})
		}
		// clock stretching：SCL 低电平被从机拉长的区间
		for i := 0; i+1 < len(sclEdges); i++ {
			f, r := sclEdges[i], sclEdges[i+1]
			if f.TNs <= s.start || f.TNs >= end || f.Level != 0 || r.Level != 1 {
				continue
			}
			if r.TNs-f.TNs > p.MinStretchNs {
				e.event("clock_stretch", txnID, map[string]any{
					"bus": bus.ID, "txn": txnID, "start_ns": f.TNs, "end_ns": r.TNs,
					"duration_ns": r.TNs - f.TNs,
				})
			}
		}
		var bits []bit
		broken := false
		var lastT int64
		for _, ed := range sclEdges {
			if ed.TNs <= s.start || ed.TNs >= end || ed.Level != 1 {
				continue
			}
			if lastT != 0 && ed.TNs-lastT > p.MaxGapNs {
				e.event("data_gap", txnID, map[string]any{
					"bus": bus.ID, "txn": txnID, "at_ns": lastT,
					"gap_ns": ed.TNs - lastT, "spliced": false,
					"note": "gap inside frame; trailing bits not spliced into transaction",
				})
				broken = true
				break
			}
			lastT = ed.TNs
			amb := e.coincident(sdaCh, ed.TNs)
			bits = append(bits, bit{t: ed.TNs, v: e.levelAt(sdaCh, ed.TNs, true), ambig: amb, edgeID: ed.ID})
		}
		txn := domain.Transaction{ID: txnID, Bus: "i2c", Device: bus.Label,
			StartNs: s.start, EndNs: end,
			Label: fmt.Sprintf("I2C %s seg%d", bus.Label, si)}
		e.res.Txns = append(e.res.Txns, txn)
		if broken {
			e.diag("warning", "FRAME_GAP", fmt.Sprintf("transaction %s truncated at internal gap", txnID),
				domain.Pos{Field: bus.ID})
		}
		if err := e.assembleI2CSlots(bus, txnID, bits); err != nil {
			return err
		}
		prevTxnID = txnID
		prevStopT = end
	}
	return nil
}

func (e *engine) assembleI2CSlots(bus domain.Bus, txnID string, bits []bit) error {
	idx := 0
	for off := 0; off < len(bits); off += 9 {
		grp := bits[off:min(off+9, len(bits))]
		slotID := "slot-" + domain.Fingerprint(txnID, fmt.Sprint(idx))
		kind := "data"
		if idx == 0 {
			kind = "address"
		}
		slot := domain.Slot{ID: slotID, TxnID: txnID, Idx: idx, Kind: kind,
			Ack: "none", Valid: true, StartNs: grp[0].t, EndNs: grp[len(grp)-1].t}
		for _, b := range grp {
			slot.EdgeRefs = append(slot.EdgeRefs, b.edgeID)
		}
		full := len(grp) == 9
		dataBits := grp
		if full {
			dataBits = grp[:8]
			switch {
			case grp[8].ambig:
				slot.Ack = "unknown"
			case grp[8].v == 0:
				slot.Ack = "ack"
			default:
				slot.Ack = "nack"
			}
		}
		if !full {
			slot.Valid = false
			slot.Invalid = "missing-clock-edge"
			e.event("missing_clock_edge", slotID, map[string]any{
				"bus": bus.ID, "txn": txnID, "slot": slotID,
				"expected_bits": 9, "got_bits": len(grp),
			})
		}
		nAmb := 0
		base := 0
		ambPos := []int{}
		for i, b := range dataBits {
			base = base<<1 | b.v
			if b.ambig {
				nAmb++
				ambPos = append(ambPos, i)
			}
		}
		if full && slot.Ack == "unknown" && nAmb == 0 {
			// ACK 位边界采样：保留 ack/nack 两个候选
			v := base
			slot.Byte = &v
			slot.GroupID = "grp-" + domain.Fingerprint(slotID)
			for _, ack := range []string{"ack", "nack"} {
				if err := e.addCandidate(domain.Candidate{
					ID:      "cand-" + domain.Fingerprint(slot.GroupID, ack),
					GroupID: slot.GroupID, SlotID: slotID, Label: ack,
					Value: &v, Ack: ack, Confidence: 0.5,
					Reason: "boundary-sample on ACK bit",
				}); err != nil {
					return err
				}
			}
			e.event("candidate_ambiguous", slot.GroupID, map[string]any{
				"slot": slotID, "group": slot.GroupID, "reason": "boundary-sample",
				"candidates": 2,
			})
			e.res.Slots = append(e.res.Slots, slot)
			idx++
			continue
		}
		if !full {
			// 时钟缺边：无法唯一决定时保留全部合法候选
			switch {
			case len(grp) == 8:
				v := base
				slot.Byte = &v
				slot.Ack = "unknown"
				slot.GroupID = "grp-" + domain.Fingerprint(slotID)
				for _, ack := range []string{"ack", "nack"} {
					if err := e.addCandidate(domain.Candidate{
						ID:      "cand-" + domain.Fingerprint(slot.GroupID, ack),
						GroupID: slot.GroupID, SlotID: slotID, Label: ack,
						Value: &v, Ack: ack, Confidence: 0.5,
						Reason: "missing-clock-edge on ACK bit",
					}); err != nil {
						return err
					}
				}
			case len(grp) == 7:
				slot.Ack = "unknown"
				slot.GroupID = "grp-" + domain.Fingerprint(slotID)
				for _, tail := range []int{0, 1} {
					v := base<<1 | tail
					if err := e.addCandidate(domain.Candidate{
						ID:      "cand-" + domain.Fingerprint(slot.GroupID, fmt.Sprint(v)),
						GroupID: slot.GroupID, SlotID: slotID, Label: fmt.Sprintf("0x%02X", v),
						Value: &v, Ack: "unknown", Confidence: 0.5,
						Reason: "missing-clock-edge on data bit",
					}); err != nil {
						return err
					}
				}
			default:
				e.diag("error", "FRAME_TOO_SHORT",
					fmt.Sprintf("slot %s has only %d bits; no legal candidates", slotID, len(grp)),
					domain.Pos{Field: bus.ID})
			}
			e.res.Slots = append(e.res.Slots, slot)
			idx++
			continue
		}
		if nAmb == 0 {
			v := base
			slot.Byte = &v
			e.res.Slots = append(e.res.Slots, slot)
			idx++
			continue
		}
		// 边界采样：SDA 与 SCL 上升沿重合，保留并列候选
		combos := 1 << nAmb
		if combos > 8 {
			combos = 8
		}
		slot.GroupID = "grp-" + domain.Fingerprint(slotID)
		conf := 1.0 / float64(combos)
		for c := 0; c < combos; c++ {
			v := base
			for j, pos := range ambPos {
				if c&(1<<j) != 0 {
					v ^= 1 << (7 - pos)
				}
			}
			if err := e.addCandidate(domain.Candidate{
				ID:      "cand-" + domain.Fingerprint(slot.GroupID, fmt.Sprint(v), slot.Ack),
				GroupID: slot.GroupID, SlotID: slotID, Label: fmt.Sprintf("0x%02X", v),
				Value: &v, Ack: slot.Ack, Confidence: conf,
				Reason: "boundary-sample: SDA edge coincides with SCL rising edge",
			}); err != nil {
				return err
			}
		}
		e.event("candidate_ambiguous", slot.GroupID, map[string]any{
			"slot": slotID, "group": slot.GroupID, "reason": "boundary-sample",
			"candidates": combos,
		})
		e.res.Slots = append(e.res.Slots, slot)
		idx++
	}
	return nil
}

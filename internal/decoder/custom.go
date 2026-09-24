package decoder

import (
	"fmt"

	"edgebench/internal/domain"
)

// decodeCustom 自定义同步总线：可配置采样沿；采样沿不明时保留多个候选。
func (e *engine) decodeCustom(bus domain.Bus) error {
	clkCh, dataCh := bus.Channels["clk"], bus.Channels["data"]
	p := e.in.Params.Custom
	clkEdges := e.edgesBy[clkCh]
	// 帧切分：时钟空闲超过 max_gap 视为帧间隔
	type frame struct{ start, end int64 }
	var frames []frame
	var cur *frame
	var lastT int64
	for _, ed := range clkEdges {
		if cur == nil || ed.TNs-lastT > p.MaxGapNs {
			frames = append(frames, frame{start: ed.TNs})
			cur = &frames[len(frames)-1]
		}
		cur.end = ed.TNs
		lastT = ed.TNs
	}
	edges := []string{}
	switch p.SampleEdge {
	case "rising":
		edges = []string{"rising"}
	case "falling":
		edges = []string{"falling"}
	default:
		edges = []string{"rising", "falling"}
	}
	for fi, fr := range frames {
		txnID := "txn-" + domain.Fingerprint(bus.ID, bus.Label, fmt.Sprint(fr.start), fmt.Sprint(fr.end), fmt.Sprint(fi))
		e.res.Txns = append(e.res.Txns, domain.Transaction{
			ID: txnID, Bus: "custom", Device: bus.Label,
			StartNs: fr.start, EndNs: fr.end,
			Label: fmt.Sprintf("SYNC %s frame%d", bus.Label, fi),
		})
		var frameClk []domain.Edge
		for _, ed := range clkEdges {
			if ed.TNs >= fr.start && ed.TNs <= fr.end {
				frameClk = append(frameClk, ed)
			}
		}
		bitsByEdge := map[string][]bit{}
		for _, which := range edges {
			var bits []bit
			for _, ed := range frameClk {
				if (which == "rising") != (ed.Level == 1) {
					continue
				}
				amb := e.coincident(dataCh, ed.TNs)
				bits = append(bits, bit{t: ed.TNs, v: e.levelAt(dataCh, ed.TNs, true), ambig: amb, edgeID: ed.ID})
			}
			bitsByEdge[which] = bits
		}
		nBits := len(bitsByEdge[edges[0]])
		nFull := nBits / 8
		rem := nBits % 8
		for idx := 0; idx < nFull; idx++ {
			slotID := "slot-" + domain.Fingerprint(txnID, fmt.Sprint(idx))
			slot := domain.Slot{ID: slotID, TxnID: txnID, Idx: idx, Kind: "data",
				Ack: "none", Valid: true}
			vals := map[string]int{}
			amb := false
			for _, which := range edges {
				bits := bitsByEdge[which][idx*8 : idx*8+8]
				v := 0
				for _, b := range bits {
					v = v<<1 | b.v
					amb = amb || b.ambig
				}
				vals[which] = v
				if idx == 0 {
					slot.StartNs = bits[0].t
					slot.EndNs = bits[7].t
				}
			}
			for _, b := range bitsByEdge[edges[0]][idx*8 : idx*8+8] {
				slot.EdgeRefs = append(slot.EdgeRefs, b.edgeID)
			}
			if len(edges) == 1 && !amb {
				v := vals[edges[0]]
				slot.Byte = &v
				e.res.Slots = append(e.res.Slots, slot)
				continue
			}
			slot.GroupID = "grp-" + domain.Fingerprint(slotID)
			// 合并相同取值的候选
			type key struct{ v int }
			seen := map[key][]string{}
			var order []key
			for _, which := range edges {
				k := key{vals[which]}
				if _, ok := seen[k]; !ok {
					order = append(order, k)
				}
				seen[k] = append(seen[k], which)
			}
			conf := 1.0 / float64(len(order))
			for _, k := range order {
				v := k.v
				reason := "sample-edge-unknown"
				if amb {
					reason = "sample-edge-unknown + boundary-sample"
				}
				if err := e.addCandidate(domain.Candidate{
					ID:      "cand-" + domain.Fingerprint(slot.GroupID, fmt.Sprint(v)),
					GroupID: slot.GroupID, SlotID: slotID,
					Label: fmt.Sprintf("0x%02X %v", v, seen[k]),
					Value: &v, Ack: "none", Confidence: conf, Reason: reason,
				}); err != nil {
					return err
				}
			}
			e.event("candidate_ambiguous", slot.GroupID, map[string]any{
				"slot": slotID, "group": slot.GroupID, "reason": "sample-edge-unknown",
				"candidates": len(order),
			})
			e.res.Slots = append(e.res.Slots, slot)
		}
		if rem > 0 {
			slotID := "slot-" + domain.Fingerprint(txnID, fmt.Sprint(nFull))
			bits := bitsByEdge[edges[0]][nFull*8:]
			slot := domain.Slot{ID: slotID, TxnID: txnID, Idx: nFull, Kind: "data",
				Ack: "none", Valid: false, Invalid: "missing-clock-edge",
				StartNs: bits[0].t, EndNs: bits[len(bits)-1].t}
			for _, b := range bits {
				slot.EdgeRefs = append(slot.EdgeRefs, b.edgeID)
			}
			e.event("missing_clock_edge", slotID, map[string]any{
				"bus": bus.ID, "txn": txnID, "slot": slotID,
				"expected_bits": 8, "got_bits": rem,
			})
			e.res.Slots = append(e.res.Slots, slot)
		}
	}
	return nil
}

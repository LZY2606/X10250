package decoder

import (
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

func (e *engine) decodeSPI(bus domain.Bus) error {
	sclkCh, mosiCh := bus.Channels["sclk"], bus.Channels["mosi"]
	p := e.in.Params.SPI
	var csList []string
	for role := range bus.Channels {
		if len(role) >= 2 && role[:2] == "cs" {
			csList = append(csList, role)
		}
	}
	sort.Strings(csList)
	if len(csList) > 1 {
		e.event("multi_device_cs", bus.ID, map[string]any{
			"bus": bus.ID, "devices": csList,
			"note": "multiple chip-select lines; frames decoded per device",
		})
	}
	sclkEdges := e.edgesBy[sclkCh]
	for _, csRole := range csList {
		csCh := bus.Channels[csRole]
		device := bus.Label + "/" + csRole
		// 片选有效（低电平）区间
		type span struct{ start, end int64 }
		var spans []span
		open := false
		var cur span
		for _, ed := range e.edgesBy[csCh] {
			if ed.Level == 0 && !open {
				cur = span{start: ed.TNs}
				open = true
			} else if ed.Level == 1 && open {
				cur.end = ed.TNs
				spans = append(spans, cur)
				open = false
			}
		}
		if open {
			if len(sclkEdges) > 0 {
				cur.end = sclkEdges[len(sclkEdges)-1].TNs
				spans = append(spans, cur)
			}
		}
		var prevEnd int64
		var prevTxn string
		for si, sp := range spans {
			txnID := "txn-" + domain.Fingerprint(bus.ID, device, fmt.Sprint(sp.start), fmt.Sprint(sp.end), fmt.Sprint(si))
			if prevTxn != "" && sp.start-prevEnd > p.MaxGapNs {
				e.event("data_gap", txnID, map[string]any{
					"bus": bus.ID, "device": device, "left_txn": prevTxn, "right_txn": txnID,
					"gap_ns": sp.start - prevEnd, "spliced": false,
					"note": "gap between CS frames exceeds max_gap; frames kept separate",
				})
			}
			var clocks []domain.Edge
			for _, ed := range sclkEdges {
				if ed.TNs > sp.start && ed.TNs < sp.end {
					clocks = append(clocks, ed)
				}
			}
			e.res.Txns = append(e.res.Txns, domain.Transaction{
				ID: txnID, Bus: "spi", Device: device,
				StartNs: sp.start, EndNs: sp.end,
				Label: fmt.Sprintf("SPI %s frame%d", device, si),
			})
			if err := e.assembleSPISlots(bus, txnID, device, mosiCh, clocks); err != nil {
				return err
			}
			prevEnd = sp.end
			prevTxn = txnID
		}
	}
	return nil
}

// sampleForMode 返回该 SPI mode 的采样沿（rising=true）。
func sampleRising(mode int) bool {
	cpha := mode & 1
	cpol := (mode >> 1) & 1
	// CPHA=0 采样第一沿：CPOL=0 上升沿，CPOL=1 下降沿；CPHA=1 相反
	return (cpha == 0) == (cpol == 0)
}

func (e *engine) assembleSPISlots(bus domain.Bus, txnID, device, mosiCh string, clocks []domain.Edge) error {
	p := e.in.Params.SPI
	modes := []int{}
	if p.Mode == nil {
		modes = []int{0, 1, 2, 3}
		e.event("spi_mode_unknown", txnID, map[string]any{
			"bus": bus.ID, "txn": txnID,
			"note": "SPI mode not pinned; keeping candidates for all four modes",
		})
	} else {
		modes = []int{*p.Mode}
	}
	bitsByMode := map[int][]bit{}
	for _, m := range modes {
		rising := sampleRising(m)
		var bits []bit
		for _, ed := range clocks {
			if (ed.Level == 1) != rising {
				continue
			}
			amb := e.coincident(mosiCh, ed.TNs)
			bits = append(bits, bit{t: ed.TNs, v: e.levelAt(mosiCh, ed.TNs, true), ambig: amb, edgeID: ed.ID})
		}
		bitsByMode[m] = bits
	}
	nBits := len(bitsByMode[modes[0]])
	nFull := nBits / 8
	rem := nBits % 8
	for idx := 0; idx < nFull; idx++ {
		slotID := "slot-" + domain.Fingerprint(txnID, fmt.Sprint(idx))
		slot := domain.Slot{ID: slotID, TxnID: txnID, Idx: idx, Kind: "data",
			Ack: "none", Valid: true}
		// 每个 mode 独立求值，再按值合并候选
		type key struct {
			v   int
			amb bool
		}
		merged := map[key][]int{}
		var order []key
		for _, m := range modes {
			bits := bitsByMode[m][idx*8 : idx*8+8]
			v := 0
			amb := false
			for _, b := range bits {
				v = v<<1 | b.v
				amb = amb || b.ambig
			}
			k := key{v, amb}
			if _, ok := merged[k]; !ok {
				order = append(order, k)
			}
			merged[k] = append(merged[k], m)
			if idx == 0 {
				slot.StartNs = bits[0].t
				slot.EndNs = bits[7].t
			}
		}
		for _, b := range bitsByMode[modes[0]][idx*8 : idx*8+8] {
			slot.EdgeRefs = append(slot.EdgeRefs, b.edgeID)
		}
		if len(order) == 1 && !order[0].amb {
			v := order[0].v
			slot.Byte = &v
			e.res.Slots = append(e.res.Slots, slot)
			continue
		}
		slot.GroupID = "grp-" + domain.Fingerprint(slotID)
		conf := 1.0 / float64(len(order))
		for _, k := range order {
			v := k.v
			ms := merged[k]
			lbl := fmt.Sprintf("0x%02X mode", v)
			for i, m := range ms {
				if i == 0 {
					lbl += fmt.Sprint(m)
				} else {
					lbl += fmt.Sprintf("/%d", m)
				}
			}
			reason := "spi-mode-unknown"
			if k.amb {
				reason = "spi-mode-unknown + boundary-sample"
			}
			if err := e.addCandidate(domain.Candidate{
				ID:      "cand-" + domain.Fingerprint(slot.GroupID, fmt.Sprint(v), lbl),
				GroupID: slot.GroupID, SlotID: slotID, Label: lbl,
				Value: &v, Ack: "none", Confidence: conf, Reason: reason,
			}); err != nil {
				return err
			}
		}
		e.event("candidate_ambiguous", slot.GroupID, map[string]any{
			"slot": slotID, "group": slot.GroupID, "reason": "spi-mode-unknown",
			"candidates": len(order),
		})
		e.res.Slots = append(e.res.Slots, slot)
	}
	if rem > 0 {
		slotID := "slot-" + domain.Fingerprint(txnID, fmt.Sprint(nFull))
		slot := domain.Slot{ID: slotID, TxnID: txnID, Idx: nFull, Kind: "data",
			Ack: "none", Valid: false, Invalid: "missing-clock-edge"}
		bits := bitsByMode[modes[0]][nFull*8:]
		slot.StartNs = bits[0].t
		slot.EndNs = bits[len(bits)-1].t
		for _, b := range bits {
			slot.EdgeRefs = append(slot.EdgeRefs, b.edgeID)
		}
		e.event("missing_clock_edge", slotID, map[string]any{
			"bus": bus.ID, "txn": txnID, "slot": slotID,
			"expected_bits": 8, "got_bits": rem,
		})
		if rem == 7 {
			base := 0
			for _, b := range bits {
				base = base<<1 | b.v
			}
			slot.GroupID = "grp-" + domain.Fingerprint(slotID)
			for _, tail := range []int{0, 1} {
				v := base<<1 | tail
				if err := e.addCandidate(domain.Candidate{
					ID:      "cand-" + domain.Fingerprint(slot.GroupID, fmt.Sprint(v)),
					GroupID: slot.GroupID, SlotID: slotID, Label: fmt.Sprintf("0x%02X", v),
					Value: &v, Ack: "none", Confidence: 0.5,
					Reason: "missing-clock-edge on data bit",
				}); err != nil {
					return err
				}
			}
		}
		e.res.Slots = append(e.res.Slots, slot)
	}
	return nil
}

package decode

import (
	"fmt"

	"edgetrace/internal/model"
)

// bitCell is one captured SDA bit at an active SCL edge.
type bitCell struct {
	ns       model.Rat
	bit      int
	sclEdge  string
	dataEdge string // SDA edge coincident with the sampling edge, if any
}

// frame is one START (or repeated START) region.
type frame struct {
	startNs   model.Rat
	startEdge string
	stop      bool
	stopNs    model.Rat
	stopEdge  string
	cells     []bitCell
	gapSplit  bool
}

func (st *i2cState) run() {
	prevSCL, prevSDA := 1, 1
	cur := (*frame)(nil)
	var lastActive *model.Rat
	var lastFall *model.Rat
	var lastRise *model.Rat
	half := model.Rat{}

	for i := 0; i < len(st.evs); i++ {
		ev := st.evs[i]

		// Track clock period from consecutive active edges.
		switch {
		case st.h.activeUp && ev.sclLvl == 1 && len(ev.sclIDs) > 0:
			if lastRise != nil {
				d := ev.ns.Sub(*lastRise)
				if half.IsZero() || d.Cmp(half.Mul(model.RatInt(2))) < 0 {
					if h, err := model.NewRat(d.Num(), d.Den()*2); err == nil {
						half = h
					}
				}
			}
			t := ev.ns
			lastRise = &t
		case !st.h.activeUp && ev.sclLvl == 0 && len(ev.sclIDs) > 0:
			if lastFall != nil {
				d := ev.ns.Sub(*lastFall)
				if half.IsZero() || d.Cmp(half.Mul(model.RatInt(2))) < 0 {
					if h, err := model.NewRat(d.Num(), d.Den()*2); err == nil {
						half = h
					}
				}
			}
			t := ev.ns
			lastFall = &t
		}

		// START / repeated START: SDA falls while SCL stays high.
		if prevSCL == 1 && ev.sdaLvl == 0 && prevSDA == 1 && len(ev.sdaIDs) > 0 {
			if cur != nil {
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CRepeated, Bus: st.x.bus.Name, Severity: "info",
					StartEdge: ev.sdaIDs[0], StartNs: &ev.ns,
					EdgeIDs: joinIDs(ev.sdaIDs, ev.sclIDs), Message: "repeated START",
				})
				st.frames = append(st.frames, *cur)
			} else {
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CStart, Bus: st.x.bus.Name, Severity: "info",
					StartEdge: ev.sdaIDs[0], StartNs: &ev.ns,
					EdgeIDs: joinIDs(ev.sdaIDs, ev.sclIDs), Message: "START",
				})
			}
			cur = &frame{startNs: ev.ns, startEdge: ev.sdaIDs[0]}
			lastActive = nil
		}

		// STOP: SDA rises while SCL stays high, inside a frame.
		if cur != nil && prevSCL == 1 && ev.sdaLvl == 1 && prevSDA == 0 && len(ev.sdaIDs) > 0 {
			cur.stop = true
			cur.stopNs = ev.ns
			cur.stopEdge = ev.sdaIDs[0]
			st.frames = append(st.frames, *cur)
			cur = nil
			st.evid = append(st.evid, model.Evidence{
				Code: EvI2CStop, Bus: st.x.bus.Name, Severity: "info",
				StartEdge: ev.sdaIDs[0], StartNs: &ev.ns,
				EdgeIDs: joinIDs(ev.sdaIDs, ev.sclIDs), Message: "STOP",
			})
			lastActive = nil
		}

		// Active clock edge inside a frame: capture one bit.
		active := st.h.activeUp && ev.sclLvl == 1 && len(ev.sclIDs) > 0 ||
			!st.h.activeUp && ev.sclLvl == 0 && len(ev.sclIDs) > 0
		if active && cur != nil {
			// Clock stretching: low-time beyond 2 half periods (only meaningful
			// for rising-edge active polarity; for inverted polarity we use the
			// symmetric high-time estimate).
			refStart := lastFall
			if !st.h.activeUp {
				refStart = lastRise
			}
			if refStart != nil && !half.IsZero() {
				low := ev.ns.Sub(*refStart)
				if low.Cmp(half.Mul(model.RatInt(3))) > 0 {
					st.evid = append(st.evid, model.Evidence{
						Code: EvI2CStretch, Bus: st.x.bus.Name, Severity: "info",
						StartNs: refStart, EndNs: &ev.ns, EdgeIDs: ev.sclIDs,
						Message: "clock stretching: clock held beyond 1.5 periods",
					})
				}
			}
			// Data gap: unusually large spacing between active edges; the frame
			// is split and bytes are never stitched across it.
			if lastActive != nil {
				d := ev.ns.Sub(*lastActive)
				if d.Cmp(st.gap) > 0 {
					st.evid = append(st.evid, model.Evidence{
						Code: EvI2CGap, Bus: st.x.bus.Name, Severity: "warn",
						StartNs: lastActive, EndNs: &ev.ns, EdgeIDs: ev.sclIDs,
						Message: fmt.Sprintf("data gap %s ns > tolerance %s ns; not stitched",
							d.String(), st.gap.String()),
					})
					st.frames = append(st.frames, *cur)
					cur = &frame{startNs: ev.ns, startEdge: ev.sclIDs[0], gapSplit: true}
				}
			}
			boundaryEdge := ""
			if len(ev.sdaIDs) > 0 {
				boundaryEdge = ev.sdaIDs[0]
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CBoundary, Bus: st.x.bus.Name, Severity: "warn",
					StartEdge: boundaryEdge, StartNs: &ev.ns,
					EdgeIDs: joinIDs(ev.sclIDs, ev.sdaIDs),
					Message: "SDA edge coincides with the sampling clock edge; boundary=" + st.boundary,
				})
			}
			cur.cells = append(cur.cells, bitCell{
				ns: ev.ns, bit: ev.sdaLvl,
				sclEdge: ev.sclIDs[0], dataEdge: boundaryEdge,
			})
			t := ev.ns
			lastActive = &t
		}

		if ev.sclLvl != -1 {
			prevSCL = ev.sclLvl
		}
		if ev.sdaLvl != -1 {
			prevSDA = ev.sdaLvl
		}
	}
	if cur != nil {
		st.frames = append(st.frames, *cur)
		st.evid = append(st.evid, model.Evidence{
			Code: EvI2CMissingEdge, Bus: st.x.bus.Name, Severity: "warn",
			StartNs: &cur.startNs, Message: "frame has no STOP: missing terminating edge",
		})
	}
	st.materialize()
}

// materialize turns frames into stable bytes, segments and transactions.
func (st *i2cState) materialize() {
	byteSeq, segSeq, txnSeq := 0, 0, 0
	var txnSegs []string
	var txnBytes []string
	var txnStart model.Rat
	var txnStartEdge string
	var txnEnd model.Rat
	flushTxn := func() {
		if len(txnSegs) == 0 {
			return
		}
		txnSeq++
		st.txns = append(st.txns, model.Transaction{
			ID:         stableID2("txn", st.x.bus.Name, st.h.key, txnStart.String(), txnEnd.String()),
			Bus:        st.x.bus.Name,
			SegmentIDs: txnSegs,
			ByteIDs:    txnBytes,
			StartEdge:  txnStartEdge,
			StartNs:    &txnStart,
			EndNs:      &txnEnd,
		})
		txnSegs, txnBytes = nil, nil
	}
	for fi := range st.frames {
		fr := &st.frames[fi]
		if len(txnSegs) == 0 {
			txnStart, txnStartEdge = fr.startNs, fr.startEdge
		}
		var segByteIDs []string
		for pos := 0; pos < len(fr.cells); pos += 9 {
			end := pos + 9
			if end > len(fr.cells) {
				end = len(fr.cells)
			}
			group := fr.cells[pos:end]
			val := 0
			bits := len(group)
			dataBits := bits
			ack := ""
			if bits == 9 {
				dataBits = 8
				if group[8].bit == 0 {
					ack = "ACK"
				} else {
					ack = "NACK"
				}
			}
			for i := 0; i < dataBits && i < 8; i++ {
				val = (val << 1) | group[i].bit
			}
			byteSeq++
			role := "data"
			if len(segByteIDs) == 0 {
				role = "addr"
			}
			valid := bits == 9
			why := ""
			if !valid {
				why = fmt.Sprintf("partial byte: %d data bits; missing clock edge", dataBits)
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CMissingEdge, Bus: st.x.bus.Name, Severity: "warn",
					StartNs: &group[0].ns, EndNs: &group[len(group)-1].ns,
					StartEdge: group[0].sclEdge, EndEdge: group[len(group)-1].sclEdge,
					Message: why,
				})
			}
			if ack == "ACK" {
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CAck, Bus: st.x.bus.Name, Severity: "info",
					StartNs: &group[8].ns, StartEdge: group[8].sclEdge, Message: "ACK",
				})
			} else if ack == "NACK" {
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CNack, Bus: st.x.bus.Name, Severity: "info",
					StartNs: &group[8].ns, StartEdge: group[8].sclEdge, Message: "NACK",
				})
			}
			ids := []string{}
			for _, c := range group {
				ids = append(ids, c.sclEdge)
				if c.dataEdge != "" {
					ids = append(ids, c.dataEdge)
				}
			}
			startNs, endNs := group[0].ns, group[len(group)-1].ns
			b := model.Byte{
				ID:  stableID2("byte", st.x.bus.Name, st.h.key, startNs.String(), fmt.Sprint(byteSeq)),
				Bus: st.x.bus.Name, Index: byteSeq - 1, Value: val,
				ValueText: fmt.Sprintf("0x%02X", val), Role: role, AckNack: ack,
				EdgeIDs: dedup(ids), StartEdge: group[0].sclEdge, EndEdge: group[len(group)-1].sclEdge,
				StartNs: &startNs, EndNs: &endNs, SampleMode: st.boundary,
				Valid: valid, InvalidWhy: why,
			}
			st.bytes = append(st.bytes, b)
			segByteIDs = append(segByteIDs, b.ID)
		}
		endNs := fr.stopNs
		endEdge := fr.stopEdge
		if endNs.IsZero() {
			if len(fr.cells) > 0 {
				endNs = fr.cells[len(fr.cells)-1].ns
				endEdge = fr.cells[len(fr.cells)-1].sclEdge
			} else {
				endNs = fr.startNs
				endEdge = fr.startEdge
			}
		}
		segSeq++
		seg := model.Segment{
			ID:        stableID2("seg", st.x.bus.Name, st.h.key, fr.startNs.String(), endNs.String()),
			Bus:       st.x.bus.Name,
			Kind:      "i2c_segment",
			StartEdge: fr.startEdge,
			EndEdge:   endEdge,
			StartNs:   &fr.startNs,
			EndNs:     &endNs,
			ByteIDs:   segByteIDs,
		}
		st.segs = append(st.segs, seg)
		txnSegs = append(txnSegs, seg.ID)
		txnBytes = append(txnBytes, segByteIDs...)
		txnEnd = endNs
		if fr.stop || fr.gapSplit {
			flushTxn()
		}
	}
	flushTxn()
}

func joinIDs(a, b []string) []string {
	return dedup(append(append([]string{}, a...), b...))
}

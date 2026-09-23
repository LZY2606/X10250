package decode

import (
	"fmt"

	"edgetrace/internal/model"
)

// csFrame is one chip-select assertion window. activeLow=false means the
// device selects on rising edge (selected through params.SyncPolarity override
// or role suffix "_h"); the hardware default is active-low.
type csFrame struct {
	nsStart, nsEnd model.Rat
	startEdge      string
	endEdge        string
	activeLow      bool
}

// csFrames extracts assertion windows on one CS signal.
func csFrames(x *ctx, s *Sig, role string) []csFrame {
	pol := "low"
	if v, ok := x.par.SyncPolarity[x.bus.Name+":"+role]; ok {
		pol = v
	}
	if len(role) >= 3 && role[len(role)-2:] == "_h" {
		pol = "high"
	}
	activeLow := pol != "high"
	var frames []csFrame
	var start *model.Rat
	var startEdge string
	prev := -1
	for _, e := range s.Edges() {
		if activeLow {
			if prev != 0 && e.Level == 0 {
				t := *e.OffsetNs
				start, startEdge = &t, e.ID
			} else if prev == 0 && e.Level == 1 && start != nil {
				frames = append(frames, csFrame{*start, *e.OffsetNs, startEdge, e.ID, true})
				start = nil
			}
		} else {
			if prev != 1 && e.Level == 1 {
				t := *e.OffsetNs
				start, startEdge = &t, e.ID
			} else if prev == 1 && e.Level == 0 && start != nil {
				frames = append(frames, csFrame{*start, *e.OffsetNs, startEdge, e.ID, false})
				start = nil
			}
		}
		prev = e.Level
	}
	if start != nil {
		// Select never released: frame closes at last observed clock/data edge.
		last := ""
		if clk := x.sig("clk"); clk != nil {
			if e, ok := clk.LastEdge(); ok {
				t := *e.OffsetNs
				frames = append(frames, csFrame{*start, t, startEdge, e.ID, activeLow})
				last = e.ID
			}
		}
		_ = last
	}
	return frames
}

// sampleEdge returns the clock level that samples data for the mode:
// CPHA=0 samples on the leading edge, CPHA=1 on the trailing edge.
func (st *spiState) sampleLevel() int {
	// leading edge is rising when CPOL=0, falling when CPOL=1
	leadingRises := st.h.cpol == 0
	sampleLeading := st.h.cpha == 0
	if sampleLeading {
		if leadingRises {
			return 1
		}
		return 0
	}
	if leadingRises {
		return 0
	}
	return 1
}

func (st *spiState) decodeFrame(clk, mosi *Sig, device string, f csFrame) {
	sampleLvl := st.sampleLevel()
	idle := st.h.cpol
	var cells []bitCell
	var lastSample *model.Rat
	prev := idle
	gapSplit := false
	edges := clk.Edges()
	for _, e := range edges {
		if e.OffsetNs.Cmp(f.nsStart) < 0 || e.OffsetNs.Cmp(f.nsEnd) > 0 {
			continue
		}
		if e.Level == sampleLvl && prev != sampleLvl {
			// sample MOSI at this edge; boundary rule for coincident edges
			lvl, _ := mosi.LevelAtNs(*e.OffsetNs)
			dataEdge := ""
			for _, d := range mosi.Edges() {
				if d.OffsetNs != nil && d.OffsetNs.Cmp(*e.OffsetNs) == 0 {
					dataEdge = d.ID
					break
				}
			}
			if dataEdge != "" {
				st.evid = append(st.evid, model.Evidence{
					Code: EvI2CBoundary, Bus: st.x.bus.Name, Severity: "warn",
					StartEdge: dataEdge, StartNs: e.OffsetNs, EdgeIDs: []string{e.ID, dataEdge},
					Message: "MOSI edge coincides with the SPI sampling edge; boundary=" + st.boundary,
				})
			}
			if lastSample != nil && e.OffsetNs.Sub(*lastSample).Cmp(st.gap) > 0 {
				st.evid = append(st.evid, model.Evidence{
					Code: EvSPIGap, Bus: st.x.bus.Name, Severity: "warn",
					StartNs: lastSample, EndNs: e.OffsetNs, EdgeIDs: []string{e.ID},
					Message: fmt.Sprintf("SPI data gap %s ns > tolerance %s ns; not stitched",
						e.OffsetNs.Sub(*lastSample).String(), st.gap.String()),
				})
				st.emitBytesAndFrame(device, f, cells, true)
				cells = nil
				gapSplit = true
			}
			t := *e.OffsetNs
			lastSample = &t
			cells = append(cells, bitCell{ns: *e.OffsetNs, bit: lvl, sclEdge: e.ID, dataEdge: dataEdge})
		}
		prev = e.Level
	}
	if len(cells) > 0 && len(cells)%8 != 0 {
		st.evid = append(st.evid, model.Evidence{
			Code: EvSPIMissingEdge, Bus: st.x.bus.Name, Severity: "warn",
			StartNs: &cells[0].ns, EndNs: &cells[len(cells)-1].ns,
			StartEdge: cells[0].sclEdge, EndEdge: cells[len(cells)-1].sclEdge,
			Message: fmt.Sprintf("CS frame ends with %d trailing bits; missing clock edge", len(cells)%8),
		})
	}
	st.emitBytesAndFrame(device, f, cells, gapSplit)
	st.evid = append(st.evid, model.Evidence{
		Code: EvSPIFrame, Bus: st.x.bus.Name, Severity: "info",
		StartNs: &f.nsStart, EndNs: &f.nsEnd,
		StartEdge: f.startEdge, EndEdge: f.endEdge,
		Message: "CS frame on device " + device,
		Detail: map[string]any{
			"device": device, "active_low": f.activeLow,
			"cpol": st.h.cpol, "cpha": st.h.cpha,
		},
	})
}

func (st *spiState) emitBytesAndFrame(device string, f csFrame, cells []bitCell, gap bool) {
	if len(cells) == 0 {
		return
	}
	var frameByteIDs []string
	n := 0
	for pos := 0; pos < len(cells); pos += 8 {
		end := pos + 8
		if end > len(cells) {
			end = len(cells)
		}
		group := cells[pos:end]
		val := 0
		for _, c := range group {
			val = (val << 1) | nonNeg(c.bit)
		}
		st.byteSeq++
		n++
		valid := len(group) == 8
		why := ""
		if !valid {
			why = fmt.Sprintf("partial byte: %d/8 bits; missing clock edge", len(group))
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
			ID:  stableID2("byte", st.x.bus.Name, st.h.key, device, startNs.String(), fmt.Sprint(st.byteSeq)),
			Bus: st.x.bus.Name, Index: st.byteSeq - 1, Value: val,
			ValueText: fmt.Sprintf("0x%02X", val), Role: "spi",
			EdgeIDs: dedup(ids), StartEdge: group[0].sclEdge, EndEdge: group[len(group)-1].sclEdge,
			StartNs: &startNs, EndNs: &endNs, SampleMode: st.boundary,
			Valid: valid, InvalidWhy: why,
		}
		st.bytes = append(st.bytes, b)
		frameByteIDs = append(frameByteIDs, b.ID)
	}
	seg := model.Segment{
		ID:        stableID2("seg", st.x.bus.Name, st.h.key, device, f.nsStart.String(), f.nsEnd.String(), fmt.Sprint(n)),
		Bus:       st.x.bus.Name,
		Kind:      "spi_frame",
		Device:    device,
		StartEdge: f.startEdge,
		EndEdge:   f.endEdge,
		StartNs:   &f.nsStart,
		EndNs:     &f.nsEnd,
		ByteIDs:   frameByteIDs,
	}
	st.segs = append(st.segs, seg)
	st.txns = append(st.txns, model.Transaction{
		ID:          stableID2("txn", st.x.bus.Name, st.h.key, device, f.nsStart.String(), f.nsEnd.String()),
		Bus:         st.x.bus.Name,
		SegmentIDs:  []string{seg.ID},
		ByteIDs:     frameByteIDs,
		StartEdge:   f.startEdge,
		EndEdge:     f.endEdge,
		StartNs:     &f.nsStart,
		EndNs:       &f.nsEnd,
		StitchedGap: gap,
	})
}

func nonNeg(b int) int {
	if b < 0 {
		return 0
	}
	return b
}

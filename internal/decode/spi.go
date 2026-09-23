package decode

import (
	"fmt"
	"sort"
)

// spiMode describes CPOL/CPHA sampling for one hypothesis.
type spiMode struct {
	id          int
	sampleLevel int  // clock level right after the sampling edge
	skipFirst   bool // CPHA=1: first clock edge is not a sampling edge
}

var spiModes = map[int]spiMode{
	0: {id: 0, sampleLevel: 1, skipFirst: false},
	1: {id: 1, sampleLevel: 0, skipFirst: true},
	2: {id: 2, sampleLevel: 0, skipFirst: false},
	3: {id: 3, sampleLevel: 1, skipFirst: true},
}

type spiWindow struct {
	cs    string
	start int64
	end   int64
}

func decodeSPI(m *Material, edges []NormEdge, p *Params) ([]Candidate, []Diagnostic, []Event, bool) {
	clkAlias := findChan(m, "spi", "clock", "sck")
	dataAlias := findChan(m, "spi", "mosi", "data")
	csAliases := findChans(m, "spi", "cs")
	var diags []Diagnostic
	if clkAlias == "" || dataAlias == "" || len(csAliases) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "spi_channels_missing",
			Message: "spi bus requires clock, data and at least one cs channel"})
		return nil, diags, nil, false
	}
	ch := byChannel(edges)
	clk, data := ch[clkAlias], ch[dataAlias]
	dataLevel := levelAt(data)

	// Active-low chip-select windows per device.
	var windows []spiWindow
	for _, cs := range csAliases {
		start := int64(-1)
		for _, e := range ch[cs] {
			if e.Level == 0 && start < 0 {
				start = e.TimePS
			} else if e.Level == 1 && start >= 0 {
				windows = append(windows, spiWindow{cs: cs, start: start, end: e.TimePS})
				start = -1
			}
		}
		if start >= 0 {
			end := int64(0)
			if len(clk) > 0 {
				end = clk[len(clk)-1].TimePS
			}
			windows = append(windows, spiWindow{cs: cs, start: start, end: end})
		}
	}
	sort.Slice(windows, func(a, b int) bool { return windows[a].start < windows[b].start })
	if len(windows) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "spi_no_cs_window",
			Message: "no active chip-select window found"})
		return nil, diags, nil, false
	}

	var modes []spiMode
	if p.SPIMode == "unknown" || p.SPIMode == "" {
		for i := 0; i < 4; i++ {
			modes = append(modes, spiModes[i])
		}
	} else {
		var id int
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &id); err != nil {
			diags = append(diags, Diagnostic{Severity: "error", Code: "spi_mode_invalid",
				Message: "spi_mode must be unknown|0|1|2|3, got " + p.SPIMode})
			return nil, diags, nil, false
		}
		md, ok := spiModes[id]
		if !ok {
			diags = append(diags, Diagnostic{Severity: "error", Code: "spi_mode_invalid",
				Message: "spi_mode out of range: " + p.SPIMode})
			return nil, diags, nil, false
		}
		modes = []spiMode{md}
	}

	var cands []Candidate
	var events []Event
	anyBytes := false
	unknownMode := p.SPIMode == "unknown" || p.SPIMode == ""

	for _, w := range windows {
		var wclk []NormEdge
		for _, e := range clk {
			if e.TimePS >= w.start && e.TimePS <= w.end {
				wclk = append(wclk, e)
			}
		}
		if len(wclk) == 0 {
			t := w.start
			diags = append(diags, Diagnostic{Severity: "warning", Code: "spi_window_no_clock",
				Message: "cs window without clock edges", Channel: w.cs, TimePS: &t})
			continue
		}
		// Split the window on internal data gaps; never stitch across.
		segments := [][]NormEdge{nil}
		for _, e := range wclk {
			cur := segments[len(segments)-1]
			if len(cur) > 0 && e.TimePS-cur[len(cur)-1].TimePS > p.GapThresholdPS {
				events = append(events, Event{Kind: EvGapSplit, Detail: map[string]any{
					"bus": "spi", "cs": w.cs, "gap_start_ps": cur[len(cur)-1].TimePS,
					"gap_end_ps": e.TimePS, "reason": "data_gap_not_stitched"}})
				segments = append(segments, nil)
			}
			segments[len(segments)-1] = append(segments[len(segments)-1], e)
		}
		if unknownMode {
			events = append(events, Event{Kind: EvCandidateRetained, Code: ReasonSPIModeUnknown,
				Detail: map[string]any{"bus": "spi", "cs": w.cs, "window_start_ps": w.start,
					"modes": []int{0, 1, 2, 3}, "alternatives": len(modes)}})
		}
		for _, md := range modes {
			txs, boundaryHits := spiDecodeSegments(segments, data, dataLevel, w.cs, md, p, &diags)
			conf := 0.95
			if unknownMode {
				conf = 0.3
			}
			cands = append(cands, Candidate{
				ID:  shortID("cand", fmt.Sprintf("spi|%s|%d|%d|%d", w.cs, md.id, w.start, len(txs))),
				Bus: "spi", Confidence: conf,
				Reasons:      modeReasons(unknownMode),
				Params:       map[string]string{"spi_mode": fmt.Sprint(md.id), "cs": w.cs},
				Transactions: txs,
			})
			anyBytes = anyBytes || countBytes(txs) > 0
			// Boundary sampling keeps a flipped-bit candidate per hit.
			for _, hit := range boundaryHits {
				altTxs, _ := spiDecodeSegmentsFlip(segments, data, dataLevel, w.cs, md, p, &diags, hit)
				cands = append(cands, Candidate{
					ID:  shortID("cand", fmt.Sprintf("spi|%s|%d|%d|flip%d", w.cs, md.id, w.start, hit)),
					Bus: "spi", Confidence: 0.2,
					Reasons:      append(modeReasons(unknownMode), ReasonBoundarySample),
					Params:       map[string]string{"spi_mode": fmt.Sprint(md.id), "cs": w.cs, "flip_bit_at_ps": fmt.Sprint(hit)},
					Transactions: altTxs,
				})
				events = append(events, Event{Kind: EvCandidateRetained, Code: ReasonBoundarySample,
					Detail: map[string]any{"bus": "spi", "cs": w.cs, "time_ps": hit, "alternatives": 2}})
			}
		}
	}
	return cands, diags, events, anyBytes
}

func modeReasons(unknown bool) []string {
	if unknown {
		return []string{ReasonSPIModeUnknown}
	}
	return []string{}
}

func spiSampleEdges(seg []NormEdge, md spiMode) []NormEdge {
	var out []NormEdge
	for i, e := range seg {
		if md.skipFirst && i == 0 {
			continue
		}
		if e.Level == md.sampleLevel {
			out = append(out, e)
		}
	}
	return out
}

func spiDecodeSegments(segments [][]NormEdge, data []NormEdge, dataLevel func(int64) int, cs string, md spiMode, p *Params, diags *[]Diagnostic) ([]Transaction, []int64) {
	return spiDecode(segments, data, dataLevel, cs, md, p, diags, -1)
}

func spiDecodeSegmentsFlip(segments [][]NormEdge, data []NormEdge, dataLevel func(int64) int, cs string, md spiMode, p *Params, diags *[]Diagnostic, flipAt int64) ([]Transaction, []int64) {
	return spiDecode(segments, data, dataLevel, cs, md, p, diags, flipAt)
}

func spiDecode(segments [][]NormEdge, data []NormEdge, dataLevel func(int64) int, cs string, md spiMode, p *Params, diags *[]Diagnostic, flipAt int64) ([]Transaction, []int64) {
	var txs []Transaction
	var boundary []int64
	for _, seg := range segments {
		samps := spiSampleEdges(seg, md)
		if len(samps) == 0 {
			continue
		}
		var bits []i2cBit
		for _, s := range samps {
			if flipAt < 0 {
				for _, e := range data {
					d := e.TimePS - s.TimePS
					if d < 0 {
						d = -d
					}
					if d <= p.BoundaryWindowPS {
						boundary = append(boundary, s.TimePS)
						break
					}
				}
			}
			lvl := dataLevel(s.TimePS)
			if lvl < 0 {
				lvl = 0
			}
			if s.TimePS == flipAt {
				lvl = 1 - lvl
			}
			bits = append(bits, i2cBit{t: s.TimePS, val: lvl, edgeIdx: s.Index})
		}
		tx := Transaction{Decoder: "spi", Device: cs, StartPS: samps[0].TimePS, EndPS: samps[len(samps)-1].TimePS}
		full := len(bits) / 8
		for i := 0; i < full; i++ {
			grp := bits[i*8 : (i+1)*8]
			val := 0
			for _, b := range grp {
				val = (val << 1) | b.val
			}
			tx.Bytes = append(tx.Bytes, Byte{
				Value: val, EdgeStart: grp[0].edgeIdx, EdgeEnd: grp[7].edgeIdx,
				TimeStartPS: grp[0].t, TimeEndPS: grp[7].t, CS: cs, Confidence: 1,
			})
		}
		if rem := len(bits) % 8; rem != 0 {
			t := bits[len(bits)-1].t
			*diags = append(*diags, Diagnostic{Severity: "warning", Code: "partial_byte",
				Message: fmt.Sprintf("%d dangling bits dropped at cs boundary", rem),
				Channel: cs, TimePS: &t})
		}
		if len(tx.Bytes) > 0 {
			tx.ID = shortID("tx", fmt.Sprintf("spi|%s|%d|%d|%d", cs, md.id, tx.StartPS, tx.EndPS))
			txs = append(txs, tx)
		}
	}
	return txs, boundary
}

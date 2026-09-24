package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// spiModes enumerates the legal CPOL/CPHA combinations. When the parameter
// version fixes polarity/phase there is one candidate; otherwise every mode
// consistent with the observed idle level is retained.
type spiMode struct {
	cpol, cpha int
	name       string
}

func decodeSPI(res *Result, sigs map[string]*Sig, p ParamsView, budget Budget) error {
	clk := sigs["sclk"]
	data := sigs["mosi"]
	period := clockPeriod(clk)
	gap := period * 4
	if q := p.GapThreshold(); q.OK && q.Value > 0 {
		gap = q.Value
	}
	// idle level observation
	idle := observedIdle(clk)
	modes := selectSPIModes(p, idle)

	csSigs := map[string]*Sig{}
	csNames := []string{}
	for role, s := range sigs {
		if len(role) >= 2 && role[:2] == "cs" {
			csSigs[role] = s
			csNames = append(csNames, role)
		}
	}
	sort.Strings(csNames)
	if len(csNames) > 0 {
		res.Evidence = append(res.Evidence, Evidence{Code: "multi-cs",
			Detail: fmt.Sprintf("%d chip-select devices: %v", len(csNames), csNames)})
	}
	msbFirst := p.BitOrder() != "lsb"

	for _, m := range modes {
		if err := budget.Use(len(clk.Times)); err != nil {
			return err
		}
		var txns []Transaction
		sampleRise := m.cpol == m.cpha // mode 0/3 sample on rising; 1/2 on falling
		if len(csNames) == 0 {
			t := runSPIFrames("spi", "", clk, data, m, sampleRise, msbFirst, gap, period, 0)
			txns = append(txns, t...)
		} else {
			for _, csn := range csNames {
				frames := framesDuringCS(csSigs[csn], clk)
				for _, fr := range frames {
					t := runSPIFrames("spi", deviceName(csn), clk, data, m, sampleRise, msbFirst, gap, period,
						len(txns), fr[0], fr[1])
					txns = append(txns, t...)
				}
			}
		}
		conf := spiConfidence(m, idle)
		res.Candidates = append(res.Candidates, Candidate{
			Key:          fmt.Sprintf("spi:CPOL=%d,CPHA=%d", m.cpol, m.cpha),
			Label:        fmt.Sprintf("SPI %s (CPOL=%d CPHA=%d)", m.name, m.cpol, m.cpha),
			Confidence:   conf,
			Transactions: txns,
		})
	}
	return nil
}

func deviceName(csRole string) string {
	switch csRole {
	case "cs0":
		return "FLASH"
	case "cs1":
		return "RADIO"
	}
	return csRole
}

func selectSPIModes(p ParamsView, idle int) []spiMode {
	all := []spiMode{
		{0, 0, "mode-0"}, {0, 1, "mode-1"}, {1, 0, "mode-2"}, {1, 1, "mode-3"},
	}
	cpol := -1
	switch p.ClockPolarity() {
	case "idle-low":
		cpol = 0
	case "idle-high":
		cpol = 1
	}
	cpha := -1
	switch p.ClockPhase() {
	case "sample-first":
		cpha = 0
	case "sample-second":
		cpha = 1
	}
	if cpol >= 0 && cpha >= 0 {
		return []spiMode{{cpol, cpha, fmt.Sprintf("mode-%d", cpol*2+cpha)}}
	}
	var out []spiMode
	for _, m := range all {
		if cpol >= 0 && m.cpol != cpol {
			continue
		}
		if cpha >= 0 && m.cpha != cpha {
			continue
		}
		if cpol < 0 && idle >= 0 && m.cpol != idle && cpha < 0 {
			// only drop when phase is also free; idle pins CPOL.
		}
		out = append(out, m)
	}
	if cpol < 0 && idle >= 0 {
		filtered := out[:0]
		for _, m := range out {
			if m.cpol == idle {
				filtered = append(filtered, m)
			}
		}
		if len(filtered) > 0 {
			out = filtered
		}
	}
	return out
}

func observedIdle(clk *Sig) int {
	if len(clk.Times) == 0 {
		return -1
	}
	// level before first edge assumed 0; use level just after last edge vs. majority dwell.
	lo, hi := 0.0, 0.0
	prev := clk.Times[0]
	prevL := 0
	for i, t := range clk.Times {
		d := t - prev
		if prevL == 0 {
			lo += d
		} else {
			hi += d
		}
		prev, prevL = t, clk.Levels[i]
	}
	if hi > lo*1.5 {
		return 1
	}
	if lo > hi*1.5 {
		return 0
	}
	return -1
}

// framesDuringCS returns [start,end] windows where CS is active (low).
func framesDuringCS(cs *Sig, clk *Sig) [][2]float64 {
	var fr [][2]float64
	start := -1.0
	for i, t := range cs.Times {
		if cs.Levels[i] == 0 && start < 0 {
			start = t
		}
		if cs.Levels[i] == 1 && start >= 0 {
			fr = append(fr, [2]float64{start, t})
			start = -1
		}
	}
	if start >= 0 {
		end := start
		if len(clk.Times) > 0 {
			end = clk.Times[len(clk.Times)-1]
		}
		fr = append(fr, [2]float64{start, end})
	}
	return fr
}

func runSPIFrames(kind model.Kind, device string, clk, data *Sig, m spiMode,
	sampleRise, msbFirst bool, gap, period float64, idxBase int, window ...float64) []Transaction {
	lo, hi := -1e30, 1e30
	if len(window) >= 2 {
		lo, hi = window[0], window[1]
	}
	var bits []Bit
	lastClock := -1.0
	var evs []Evidence
	for i, t := range clk.Times {
		if t < lo || t > hi {
			continue
		}
		edgeLevel := clk.Levels[i]
		want := 0
		if sampleRise {
			want = 1
		}
		if edgeLevel != want {
			continue
		}
		if lastClock >= 0 && period > 0 && t-lastClock > gap {
			evs = append(evs, Evidence{Code: "data-gap", StartSec: lastClock, EndSec: t,
				Detail: fmt.Sprintf("CS frame clock gap %s; bytes not spliced", fmtSec(t-lastClock))})
			// keep bits of the gap separated: flush below by resetting
			bits = append(bits, Bit{Note: "GAP", Start: lastClock, End: t, SampleAt: t})
		}
		bits = append(bits, Bit{
			Index: len(bits), Value: data.LevelAt(t), Start: t, End: nextSCLK(clk, i),
			SampleAt: t, ClockEdge: clk.Edges[i].ID,
			DataEdgeIDs: data.EdgesBetween(t-period, t+period/2),
		})
		lastClock = t
	}
	var txns []Transaction
	flush := func(group []Bit, start, end float64, extra []Evidence, invalid string) {
		nextIdx := idxBase + len(txns)
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
				byts = append(byts, Byte{Index: off / 8, Value: val, Bits: chunk,
					Start: chunk[0].Start, End: chunk[7].End, CS: device})
			}
		}
		inv := invalid
		if len(group)%8 != 0 {
			extra = append(extra, Evidence{Code: "missing-clock-edge", StartSec: group[len(group)-1].Start, EndSec: end,
				Detail: fmt.Sprintf("%d trailing clocks do not complete a byte", len(group)%8)})
			if inv == "" {
				inv = "incomplete byte at frame end"
			}
		}
		if len(byts) == 0 && inv == "" {
			inv = "no complete byte in frame"
		}
		txns = append(txns, Transaction{
			Kind: kind, Index: nextIdx, Device: device,
			Start: start, End: end, Bytes: byts, Mode: m.name,
			ValidFrom: start, ValidTo: end, Invalid: inv, Evidence: extra,
		})
	}
	var group []Bit
	gStart := -1.0
	var gEvs []Evidence
	last := -1.0
	for _, b := range bits {
		if b.Note == "GAP" {
			if len(group) > 0 {
				flush(group, gStart, last, gEvs, "")
			}
			group = nil
			gEvs = append(gEvs, Evidence{Code: "data-gap", StartSec: b.Start, EndSec: b.End, Detail: "clock gap; not spliced into one transaction"})
			gStart = -1
			last = b.End
			continue
		}
		if gStart < 0 {
			gStart = b.Start
		}
		group = append(group, b)
		last = b.End
	}
	if len(group) > 0 || len(gEvs) > 0 {
		flush(group, gStart, last, gEvs, "")
	}
	return txns
}

func nextSCLK(clk *Sig, i int) float64 {
	for j := i + 1; j < len(clk.Times); j++ {
		return clk.Times[j]
	}
	return clk.Times[i]
}

func spiConfidence(m spiMode, idle int) float64 {
	if idle < 0 {
		return 0.45
	}
	if m.cpol == idle {
		return 0.9
	}
	return 0.3
}

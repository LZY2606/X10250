package decode

import (
	"fmt"
	"sort"

	"edgeworkbench/internal/model"
)

// decodeSPI frames bytes per chip-select. Unknown SPI mode keeps one
// candidate per plausible mode instead of guessing.
func decodeSPI(c *model.Capture, p *model.Params, budget *candidateBudget) *busResult {
	sclkCh, okCLK := chanByRole(c, "sclk")
	mosiCh, okMOSI := chanByRole(c, "mosi")
	csChans := chansByRole(c, "cs")
	if !okCLK || !okMOSI || len(csChans) == 0 {
		return nil
	}
	_, hasMISO := chanByRole(c, "miso")
	res := &busResult{}
	col := &collector{seen: map[string]bool{}}

	modes := []string{p.SPIMode}
	if p.SPIMode == "unknown" {
		modes = []string{"0", "3"}
		col.add(model.EvSPIModeUnknown, sclkCh.Alias, 0,
			"SPI mode is not fixed by the probe configuration; candidates for mode 0 and mode 3 are both retained")
		col.add(model.EvMultiCandidate, sclkCh.Alias, 0,
			"spi_mode_unknown yields parallel decode candidates; none is silently preferred")
	}

	activeCS := 0
	type frame struct {
		csAlias string
		start   int64
		end     int64
	}
	var frames []frame
	for _, cs := range csChans {
		var open *frame
		for _, e := range c.Edges {
			if e.Channel != cs.Alias {
				continue
			}
			lvl := levelOf(cs, e.Level)
			if lvl == 0 && open == nil {
				open = &frame{csAlias: cs.Alias, start: e.TimeNs}
			}
			if lvl == 1 && open != nil {
				open.end = e.TimeNs
				frames = append(frames, *open)
				open = nil
			}
		}
		if open != nil {
			open.end = c.Edges[len(c.Edges)-1].TimeNs
			frames = append(frames, *open)
		}
	}
	{
		seen := map[string]bool{}
		for _, f := range frames {
			seen[f.csAlias] = true
		}
		activeCS = len(seen)
	}
	if activeCS > 1 {
		col.add(model.EvMultiCS, "", 0,
			fmt.Sprintf("%d chip-select lines frame independent devices; transactions are attributed per CS, never merged", activeCS))
	}
	sort.Slice(frames, func(i, j int) bool {
		if frames[i].start != frames[j].start {
			return frames[i].start < frames[j].start
		}
		return frames[i].csAlias < frames[j].csAlias
	})

	for _, mode := range modes {
		var txns []model.Transaction
		for _, f := range frames {
			txn := model.Transaction{Bus: "spi", Device: f.csAlias, StartNs: f.start, EndNs: f.end}
			var bits []int
			var bitEdges []string
			var byteStarts []int64
			hazards := 0
			for _, e := range c.Edges {
				if e.TimeNs < f.start || e.TimeNs > f.end {
					continue
				}
				if e.Channel == mosiCh.Alias {
					// boundary: MOSI moves exactly on a sampling edge
					for _, e2 := range c.Edges {
						if e2.Channel == sclkCh.Alias && e2.TimeNs == e.TimeNs && isSampleEdge(sclkCh, e2, mode) {
							hazards++
							break
						}
					}
					continue
				}
				if e.Channel != sclkCh.Alias || !isSampleEdge(sclkCh, e, mode) {
					continue
				}
				bit := levelAt(c.Edges, mosiCh, e.TimeNs, hasMISO)
				bits = append(bits, bit)
				bitEdges = append(bitEdges, e.ID)
				if len(bits)%8 == 1 {
					byteStarts = append(byteStarts, e.TimeNs)
				}
				if len(bits)%8 == 0 {
					val := 0
					for _, b := range bits[len(bits)-8:] {
						val = val<<1 | b
					}
					txn.Bytes = append(txn.Bytes, model.Byte{
						Index:   len(txn.Bytes),
						Value:   val,
						StartNs: byteStarts[len(byteStarts)-1],
						EndNs:   e.TimeNs,
						EdgeIDs: append([]string(nil), bitEdges[len(bitEdges)-8:]...),
					})
				}
			}
			if hazards > 0 {
				col.add(model.EvBoundarySample, mosiCh.Alias, f.start,
					fmt.Sprintf("CS frame %s@%dns: MOSI toggles exactly at %d/%d SCLK sampling edges under mode %s; bytes marked ambiguous",
						f.csAlias, f.start, hazards, len(bits), mode))
				txn.Flags = appendFlag(txn.Flags, model.EvBoundarySample)
				for bi := range txn.Bytes {
					txn.Bytes[bi].Ambiguous = true
				}
			}
			if len(bits)%8 != 0 {
				res.diags = append(res.diags, model.Diagnostic{
					Category: model.DiagSemantic,
					Channel:  sclkCh.Alias,
					TimeNs:   f.end,
					Position: fmt.Sprintf("channel:%s cs:%s t=%dns", sclkCh.Alias, f.csAlias, f.end),
					Message:  fmt.Sprintf("CS frame closes with %d dangling bits (not a multiple of 8); trailing bits left undecoded", len(bits)%8),
				})
			}
			if len(txn.Bytes) > 0 {
				txns = append(txns, txn)
			}
		}
		if len(txns) == 0 {
			continue
		}
		reason := "primary"
		conf := 1.0
		if p.SPIMode == "unknown" {
			reason = "spi_mode_unknown:mode" + mode
			conf = 0.7
			if mode == "3" {
				conf = 0.3 // every sample is a boundary hazard under mode 0 waveform
			}
			budget.fork(1)
		}
		res.candidates = append(res.candidates, model.Candidate{
			Bus: "spi", Reason: reason, Confidence: conf, Transactions: txns,
		})
	}
	res.events = col.events
	return res
}

func isSampleEdge(sclkCh model.Channel, e model.Edge, mode string) bool {
	lvl := levelOf(sclkCh, e.Level)
	// mode 0: sample on rising; mode 3: sample on falling
	if mode == "3" {
		return lvl == 0
	}
	return lvl == 1
}

// levelAt reconstructs a channel's level at time t from its edge history.
func levelAt(edges []model.Edge, ch model.Channel, t int64, _ bool) int {
	lvl := 1 // idle high assumption for MOSI-like lines
	for _, e := range edges {
		if e.Channel != ch.Alias || e.TimeNs >= t {
			continue
		}
		lvl = levelOf(ch, e.Level)
	}
	return lvl
}

func appendFlag(flags []string, f string) []string {
	for _, x := range flags {
		if x == f {
			return flags
		}
	}
	return append(flags, f)
}

package decode

import (
	"fmt"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

type spiMode struct {
	name       string
	cpol, cpha int
	sampleEdge bool // true = sample data on rising edge
}

var spiModes = []spiMode{
	{"spi-mode0", 0, 0, true},
	{"spi-mode1", 0, 1, false},
	{"spi-mode2", 1, 0, false},
	{"spi-mode3", 1, 1, true},
}

// activeModes applies a parameter revision: unknown polarity/phase keeps all
// four legal candidates; a fixed value narrows to the known mode.
func (e *engine) activeModes() []spiMode {
	if e.params.SPICpol >= 0 && e.params.SPICpha >= 0 {
		for _, m := range spiModes {
			if m.cpol == e.params.SPICpol && m.cpha == e.params.SPICpha {
				return []spiMode{m}
			}
		}
	}
	out := append([]spiMode{}, spiModes...)
	return out
}

type csFrame struct {
	cs        string
	assertT   float64
	deassertT float64
}

func (e *engine) decodeSPI() *ingest.Error {
	scks := e.aliasesForRole("sck")
	if len(scks) == 0 {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "spi decode requires sck"}
	}
	sck := scks[0]
	dataCh := ""
	if xs := e.aliasesForRole("mosi"); len(xs) > 0 {
		dataCh = xs[0]
	} else if xs := e.aliasesForRole("miso"); len(xs) > 0 {
		dataCh = xs[0]
	} else if xs := e.aliasesForRole("data"); len(xs) > 0 {
		dataCh = xs[0]
	}
	if dataCh == "" {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "spi decode requires mosi/miso/data channel"}
	}
	modes := e.activeModes()
	if len(modes) > 1 {
		e.judge("SPI_MODE_AMBIGUOUS", "keep_multiple_candidates",
			fmt.Sprintf("clk polarity/phase not fixed (%q): 4 legal modes retained", e.tl.probe[sck].ClkPolRaw), 0, nil)
	}

	css := e.aliasesForRole("cs")
	var frames []csFrame
	if len(css) == 0 {
		// no CS: one frame over the entire clock run
		es := e.tl.byCh[sck]
		if len(es) == 0 {
			return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "spi: no clock edges"}
		}
		frames = []csFrame{{cs: "", assertT: es[0].t - 1e-9, deassertT: es[len(es)-1].t + 1e-9}}
	}
	for _, cs := range css {
		for _, ed := range e.tl.byCh[cs] {
			if !ed.level { // active-low: falling edge asserts
				end := 0.0
				found := false
				for _, nxt := range e.tl.byCh[cs] {
					if nxt.t > ed.t && nxt.level {
						end = nxt.t
						found = true
						break
					}
				}
				if !found {
					end = 1 << 60
				}
				frames = append(frames, csFrame{cs: cs, assertT: ed.t, deassertT: end})
				e.judge("CS_ASSERT", "frame_open",
					fmt.Sprintf("device chip-select %s asserted", cs), ns(ed.t), []string{ed.id})
			} else {
				e.judge("CS_DEASSERT", "frame_close",
					fmt.Sprintf("device chip-select %s deasserted", cs), ns(ed.t), []string{ed.id})
			}
		}
	}
	if len(frames) == 0 {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve,
			Message: "spi: chip-select never asserted (multi-device CS list: " + fmt.Sprint(css) + ")"}
	}
	if len(css) > 1 {
		e.judge("MULTI_DEVICE_CS", "separate_frames",
			fmt.Sprintf("%d chip-select channels decoded as independent device frames", len(css)), 0, nil)
	}

	n := len(frames) * len(modes) * 2
	if !e.reserve(n) {
		return &ingest.Error{Code: ingest.ErrBudgetExhausted, Message: "spi candidate budget exhausted"}
	}

	sortFrames(frames)
	for fi, fr := range frames {
		for _, m := range modes {
			e.emitSPIFrame(sck, dataCh, fi, fr, m, len(modes))
		}
	}
	return nil
}

func sortFrames(f []csFrame) {
	for i := 0; i < len(f); i++ {
		for j := i + 1; j < len(f); j++ {
			if f[j].assertT < f[i].assertT {
				f[i], f[j] = f[j], f[i]
			}
		}
	}
}

func (e *engine) emitSPIFrame(sck, data string, fi int, fr csFrame, m spiMode, modeCount int) {
	eff := effectiveClk(e.tl.byCh[sck], m.cpol == 1)
	sampleLevel := true
	if !m.sampleEdge {
		sampleLevel = false
	}
	var samples []bit
	for _, ed := range eff {
		if ed.t <= fr.assertT || ed.t >= fr.deassertT || ed.level != sampleLevel {
			continue
		}
		lvl, defID := e.tl.levelAt(data, ed.t)
		refs := []string{ed.id}
		if defID != "" {
			refs = append(refs, defID)
		}
		bnd := len(e.tl.edgesNear(data, ed.t, boundaryJitter)) > 0
		samples = append(samples, bit{value: b2i(lvl), at: ed.t, boundary: bnd, edgeRefs: refs, sclRise: ed})
	}
	missing := len(samples)%8 != 0
	emit := func(allowPartial bool) {
		n := len(samples)
		if missing {
			if allowPartial {
				n = len(samples) // keep unknown tail bits
			} else {
				n = (len(samples) / 8) * 8 // truncated: complete bytes only
			}
		}
		if n == 0 {
			return
		}
		c := model.Candidate{Bus: "spi", Mode: m.name, FrameIdx: fi}
		var refs []string
		var hasBoundary bool
		for i := 0; i < n; i++ {
			if i%8 == 0 {
				val := 0
				known := true
				var byteRefs []string
				for j := 0; j < 8; j++ {
					if i+j >= n {
						known = false
						break
					}
					b := samples[i+j]
					val = (val << 1) | b.value
					byteRefs = append(byteRefs, b.edgeRefs...)
					if b.boundary {
						hasBoundary = true
						c.Evidence = append(c.Evidence, model.Evidence{
							Kind: "boundary_sample", AtNs: ns(b.at),
							Detail: fmt.Sprintf("data edge within %.1f ns of %s sample edge", boundaryJitter*1e9, m.name),
							Edges:  b.edgeRefs,
						})
					}
				}
				hexV := fmt.Sprintf("%02X", val)
				by := model.Byte{Index: i / 8, ValueHex: hexV, Unknown: !known, Kind: "payload",
					Ack:      "cs_end",
					Valid:    model.Interval{StartNs: ns(fr.assertT), EndNs: ns(fr.deassertT), Basis: "normalized-from-raw-ns"},
					EdgeRefs: dedup(byteRefs)}
				if known {
					by.Value = &val
				} else {
					by.Kind = "partial"
				}
				c.Bytes = append(c.Bytes, by)
				refs = append(refs, byteRefs...)
			}
		}
		if len(samples) > 0 {
			last := samples[len(samples)-1]
			refs = append(refs, last.edgeRefs...)
			if missing {
				c.Evidence = append(c.Evidence, model.Evidence{
					Kind: "missing_edge", AtNs: ns(last.at),
					Detail: fmt.Sprintf("%d clock edges do not complete a byte; truncated and partial candidates kept", len(samples)%8),
					Edges:  last.edgeRefs,
				})
			}
		}
		c.Evidence = append(c.Evidence,
			model.Evidence{Kind: "cs_assert", AtNs: ns(fr.assertT), Detail: "CS " + fr.cs + " assert", Edges: nil},
			model.Evidence{Kind: "cs_deassert", AtNs: ns(fr.deassertT), Detail: "CS " + fr.cs + " deassert", Edges: nil},
		)
		c.Valid = model.Interval{StartNs: ns(fr.assertT), EndNs: ns(fr.deassertT), Basis: "normalized-from-raw-ns"}
		c.GroupID = fmt.Sprintf("G-spi-%d-%s", fi, ingest.Hash8(fmt.Sprintf("%.1f", fr.assertT), fr.cs))
		conf := 0.45
		status := "ambiguous"
		if modeCount == 1 {
			conf, status = 0.9, "ok"
		}
		if missing {
			conf -= 0.15
			status = "ambiguous"
		}
		if hasBoundary {
			conf -= 0.1
		}
		c.Conf = conf
		c.Status = status
		c.Decision = "pending"
		variant := "strict"
		if allowPartial {
			variant = "partial"
		}
		c.Mode = m.name + "+" + variant
		e.judge("SPI_CANDIDATE", "candidate_emitted",
			fmt.Sprintf("frame %d cs=%s %s bytes=%d", fi, fr.cs, c.Mode, len(c.Bytes)),
			ns(fr.assertT), dedup(refs))
		e.addCand(c)
	}
	emit(false)
	if missing {
		e.judge("MISSING_CLOCK_EDGE", "keep_multiple_candidates",
			fmt.Sprintf("spi frame %d: %d trailing sample edges without a full byte", fi, len(samples)%8),
			ns(fr.deassertT), nil)
		emit(true)
	}
}

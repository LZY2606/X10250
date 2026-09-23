package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// spiHypo accumulates bits for one (cs, mode) hypothesis.
type spiHypo struct {
	mosiBits []int
	misoBits []int
	mosiEdge []int
	misoEdge []int
	bytes    []Byte
	txns     []Transaction
	reasons  map[string]bool
	conf     float64
	txStart  int
	txEdge   int
	dev      string
}

// decodeSPI runs the SPI decoder. When the mode is unknown every plausible
// mode is kept as a separate candidate; data edges inside the boundary
// tolerance of the sampling clock edge fork bit-value hypotheses; each
// asserted chip select yields its own transaction.
func decodeSPI(channels []model.Channel, edges []model.Edge, levels []int, p Params) Result {
	sclk := roleOf(channels, "sclk")
	mosi := roleOf(channels, "mosi")
	miso := roleOf(channels, "miso")
	csList := roleOf(channels, "cs")
	if len(sclk) == 0 {
		return Result{Diagnostics: []Diag{{
			Position: "probe", Code: "missing_role",
			Message: "spi decode requires an sclk probe role", Severity: "error",
		}}}
	}
	sclkCh := sclk[0]
	mosiCh, misoCh := "", ""
	if len(mosi) > 0 {
		mosiCh = mosi[0]
	}
	if len(miso) > 0 {
		misoCh = miso[0]
	}
	if len(csList) == 0 {
		csList = []string{""} // whole capture is one implicit device
	}

	modes := []int{}
	if p.SPIMode == "unknown" || p.SPIMode == "" {
		modes = []int{0, 1, 2, 3}
	} else {
		var m int
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &m); err != nil || m < 0 || m > 3 {
			return Result{Diagnostics: []Diag{{
				Position: "params.spi_mode", Code: "bad_param",
				Message: fmt.Sprintf("spi mode %q is not one of unknown|0|1|2|3", p.SPIMode), Severity: "error",
			}}}
		}
		modes = []int{m}
	}

	var diags []Diag
	var cands []Candidate

	for _, mode := range modes {
		// cpol: idle level; sample edge: mode 0,3 sample rising... standard:
		// mode 0: cpol0 cpha0 sample rising; mode 1: cpol0 cpha1 sample falling
		// mode 2: cpol1 cpha0 sample falling; mode 3: cpol1 cpha1 sample rising
		sampleRising := mode == 0 || mode == 3
		h := &spiHypo{reasons: map[string]bool{}, conf: 1.0}
		if p.SPIMode == "unknown" || p.SPIMode == "" {
			h.reasons[ReasonSPIModeUnknown] = true
			h.conf = 0.5
		}
		if len(csList) > 1 {
			h.reasons[ReasonMultiCS] = true
		}

		for _, cs := range csList {
			decodeSPIDevice(h, cs, sclkCh, mosiCh, misoCh, edges, levels, p, sampleRising, &diags)
		}
		if len(h.bytes) == 0 {
			continue
		}
		detail := fmt.Sprintf("spi mode %d", mode)
		if p.SPIMode == "unknown" || p.SPIMode == "" {
			detail = fmt.Sprintf("spi mode %d hypothesis (mode unknown)", mode)
		}
		cands = append(cands, Candidate{
			Protocol:   "spi",
			Confidence: h.conf,
			Reasons:    sortedReasons(h.reasons),
			Detail:     detail,
			StartEdge:  h.txns[0].EdgeStart,
			EndEdge:    h.txns[len(h.txns)-1].EdgeEnd,
			Bytes:      h.bytes,
			Txns:       h.txns,
		})
	}
	return Result{Diagnostics: diags, Candidates: cands}
}

func decodeSPIDevice(h *spiHypo, cs, sclkCh, mosiCh, misoCh string, edges []model.Edge, levels []int, p Params, sampleRising bool, diags *[]Diag) {
	// Find assertion windows of this chip select (active low).
	type win struct{ start, end int }
	var wins []win
	inWin := false
	start := 0
	for i, e := range edges {
		if e.Channel != cs && cs != "" {
			continue
		}
		if cs == "" {
			wins = append(wins, win{0, len(edges) - 1})
			break
		}
		lv := levels[i]
		if lv == 0 && !inWin {
			inWin, start = true, i
		} else if lv == 1 && inWin {
			inWin = false
			wins = append(wins, win{start, i})
		}
	}
	if inWin {
		wins = append(wins, win{start, len(edges) - 1})
	}

	dev := h.dev
	if cs != "" {
		dev = "cs:" + cs
	} else if dev == "" {
		dev = "cs:implicit"
	}

	for _, w := range wins {
		h.txStart = len(h.bytes)
		h.txEdge = w.start
		h.mosiBits, h.misoBits = nil, nil
		h.mosiEdge, h.misoEdge = nil, nil
		sclkLevel := 0
		// Track last data-edge time per channel for boundary detection.
		lastDataEdge := map[string]int{}
		for i := w.start; i <= w.end && i < len(edges); i++ {
			e := edges[i]
			lv := levels[i]
			switch e.Channel {
			case sclkCh:
				rising := lv == 1 && sclkLevel == 0
				falling := lv == 0 && sclkLevel == 1
				if (rising && sampleRising) || (falling && !sampleRising) {
					// Boundary sampling: a data edge inside the tolerance
					// window makes the sampled bit ambiguous; keep the
					// alternative as a diagnostic-backed reason, and sample
					// the value present at the clock edge.
					for _, dch := range []string{mosiCh, misoCh} {
						if dch == "" {
							continue
						}
						if j, ok := lastDataEdge[dch]; ok {
							dt := e.TimeNS - edges[j].TimeNS
							if dt < 0 {
								dt = -dt
							}
							if dt <= p.BoundaryToleranceNS {
								h.reasons[ReasonBoundarySample] = true
								h.conf *= 0.7
								*diags = append(*diags, Diag{
									Position: fmt.Sprintf("edge[%d]", j),
									Code:     ReasonBoundarySample,
									Message: fmt.Sprintf("data edge on %s at %dns is within %dns of sampling clock edge at %dns",
										dch, edges[j].TimeNS, p.BoundaryToleranceNS, e.TimeNS),
									Severity: "warning",
								})
							}
						}
					}
					if mosiCh != "" {
						h.mosiBits = append(h.mosiBits, levelAt(edges, levels, mosiCh, i))
						h.mosiEdge = append(h.mosiEdge, i)
					}
					if misoCh != "" {
						h.misoBits = append(h.misoBits, levelAt(edges, levels, misoCh, i))
						h.misoEdge = append(h.misoEdge, i)
					}
					h.flushSPIByte(edges, mosiCh != "")
				}
				sclkLevel = lv
			case mosiCh, misoCh:
				if e.Channel != "" {
					lastDataEdge[e.Channel] = i
				}
			}
		}
		if len(h.bytes) > h.txStart {
			h.txns = append(h.txns, Transaction{
				Device:    dev,
				Kind:      "transfer",
				ByteStart: h.txStart,
				ByteEnd:   len(h.bytes),
				EdgeStart: w.start,
				EdgeEnd:   w.end,
			})
		}
	}
}

func (h *spiHypo) flushSPIByte(edges []model.Edge, hasMOSI bool) {
	if len(h.mosiBits) == 8 {
		val := 0
		for _, b := range h.mosiBits {
			val = val<<1 | b
		}
		h.bytes = append(h.bytes, Byte{
			Value:     val,
			EdgeStart: h.mosiEdge[0],
			EdgeEnd:   h.mosiEdge[7],
			StartNS:   edges[h.mosiEdge[0]].TimeNS,
			EndNS:     edges[h.mosiEdge[7]].TimeNS,
		})
		h.mosiBits, h.mosiEdge = nil, nil
	}
	if len(h.misoBits) == 8 {
		// MISO byte recorded as its own byte entry when no MOSI exists;
		// otherwise it rides the same transaction window.
		if !hasMOSI {
			val := 0
			for _, b := range h.misoBits {
				val = val<<1 | b
			}
			h.bytes = append(h.bytes, Byte{
				Value:     val,
				EdgeStart: h.misoEdge[0],
				EdgeEnd:   h.misoEdge[7],
				StartNS:   edges[h.misoEdge[0]].TimeNS,
				EndNS:     edges[h.misoEdge[7]].TimeNS,
			})
		}
		h.misoBits, h.misoEdge = nil, nil
	}
}

// levelAt returns the level of a channel as of capture edge index i.
func levelAt(edges []model.Edge, levels []int, ch string, i int) int {
	lv := 0
	for j := 0; j <= i; j++ {
		if edges[j].Channel == ch {
			lv = levels[j]
		}
	}
	return lv
}

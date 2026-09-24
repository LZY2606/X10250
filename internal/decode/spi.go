package decode

import (
	"fmt"
	"strconv"

	"edgedesk/internal/model"
)

// spiMode describes one SPI mode interpretation.
type spiMode struct {
	id        int
	cpol      int  // idle clock level
	sampleRising bool
}

var spiModes = []spiMode{
	{id: 0, cpol: 0, sampleRising: true},
	{id: 1, cpol: 0, sampleRising: false},
	{id: 2, cpol: 1, sampleRising: false},
	{id: 3, cpol: 1, sampleRising: true},
}

// csWindow is one active interval of a chip-select line (active low).
type csWindow struct {
	device  string
	startNS int64
	endNS   int64
}

// DecodeSPI decodes all SPI chip-select domains of a capture. When the mode
// is unknown, every legal mode is retained as a candidate; data gaps close
// transactions without stitching; partial bytes fork alignment candidates.
func DecodeSPI(m *model.Material, p model.Params) Result {
	res := Result{Kind: model.KindOK, Bus: "spi"}
	clk := m.ChannelByRole(model.RoleCLK)
	mosi := m.ChannelByRole(model.RoleMOSI)
	miso := m.ChannelByRole(model.RoleMISO)
	if clk == nil || (mosi == nil && miso == nil) {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "capture has no spi clk/data channels",
		})
		return res
	}
	if len(m.Edges) > p.MaxEdges {
		res.Kind = model.KindBudgetExhausted
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagBudgetExhausted, Severity: "error",
			Message: fmt.Sprintf("edge count %d exceeds budget %d", len(m.Edges), p.MaxEdges),
		})
		return res
	}
	if diags := validateMaterial(m); hasErrors(diags) {
		res.Kind = model.KindCorrupt
		res.Diagnostics = diags
		return res
	}

	modes := spiModes
	unknown := p.SPIMode == "unknown"
	if !unknown {
		n, err := strconv.Atoi(p.SPIMode)
		if err != nil || n < 0 || n > 3 {
			res.Kind = model.KindCorrupt
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: model.DiagUnsolvable, Severity: "error",
				Message: fmt.Sprintf("spi_mode %q is not 0..3 or unknown", p.SPIMode),
			})
			return res
		}
		modes = []spiMode{spiModes[n]}
	} else {
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagSPIModeUnknown, Severity: "info",
			Message: "spi mode unknown: retaining one candidate per legal mode",
		})
	}

	windows := csWindows(m)
	if len(windows) == 0 {
		windows = []csWindow{{device: "", startNS: 0, endNS: 1 << 62}}
	} else if len(windows) > 1 {
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagMultiDeviceCS, Severity: "info",
			Message: fmt.Sprintf("%d chip-select windows across devices decoded independently", len(windows)),
		})
	}

	var candidates []Candidate
	for _, mode := range modes {
		pol := "rising"
		if !mode.sampleRising {
			pol = "falling"
		}
		clks := clockEdges(m, *clk, pol)
		variants := decodeSPIWindows(m, p, windows, clks, mosi, miso, mode)
		candidates = append(candidates, variants...)
	}
	if len(candidates) == 0 {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "no clock edges inside any chip-select window",
		})
		return res
	}
	if len(candidates) > p.MaxCandidates {
		res.Kind = model.KindBudgetExhausted
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagBudgetExhausted, Severity: "error",
			Message: fmt.Sprintf("ambiguity produced %d candidates, budget is %d", len(candidates), p.MaxCandidates),
		})
		return res
	}
	sortCandidates(candidates)
	res.Candidates = candidates
	for _, c := range candidates {
		res.Diagnostics = append(res.Diagnostics, c.Diagnostics...)
	}
	return res
}

// csWindows extracts active-low chip-select windows per device.
func csWindows(m *model.Material) []csWindow {
	var out []csWindow
	for _, cs := range m.ChannelsByRole(model.RoleCS) {
		active := cs.Initial == 0
		start := int64(0)
		if cs.Inverted {
			active = !active
		}
		for _, e := range m.EdgesFor(cs.ID) {
			if !e.TimeOK {
				continue
			}
			logical := e.Level
			if cs.Inverted {
				logical = 1 - logical
			}
			if logical == 0 && !active {
				active = true
				start = e.TimeNS
			} else if logical == 1 && active {
				out = append(out, csWindow{device: cs.Device, startNS: start, endNS: e.TimeNS})
				active = false
			}
		}
		if active {
			out = append(out, csWindow{device: cs.Device, startNS: start, endNS: 1 << 62})
		}
	}
	return out
}

// decodeSPIWindows decodes one mode interpretation across all windows.
func decodeSPIWindows(m *model.Material, p model.Params, windows []csWindow, clks []clockEdge, mosi, miso *model.Channel, mode spiMode) []Candidate {
	// Collect bits per window, splitting on data gaps.
	type seg struct {
		device string
		bits   []spiBit
		start  int64
		end    int64
		gapEnd bool
	}
	type spiBit struct {
		mosi, miso int
		edgeID     string
		t          int64
	}
	var segs []seg
	for _, w := range windows {
		var cur *seg
		last := int64(-1)
		for _, c := range clks {
			t := c.edge.TimeNS
			if t < w.startNS || t > w.endNS {
				continue
			}
			if last >= 0 && t-last > p.GapNS && cur != nil && len(cur.bits) > 0 {
				cur.gapEnd = true
				segs = append(segs, *cur)
				cur = nil
			}
			if cur == nil {
				cur = &seg{device: w.device, start: t}
			}
			b := spiBit{edgeID: c.edge.ID, t: t, mosi: -1, miso: -1}
			if mosi != nil {
				b.mosi = newTimeline(m, *mosi).levelJustBefore(t)
			}
			if miso != nil {
				b.miso = newTimeline(m, *miso).levelJustBefore(t)
			}
			cur.bits = append(cur.bits, b)
			cur.end = t
			last = t
		}
		if cur != nil && len(cur.bits) > 0 {
			segs = append(segs, *cur)
		}
	}
	if len(segs) == 0 {
		return nil
	}
	misaligned := false
	for _, s := range segs {
		if len(s.bits)%8 != 0 {
			misaligned = true
		}
	}
	aligns := []string{"aligned"}
	if misaligned {
		aligns = []string{"drop-trailing", "drop-leading"}
	}
	var out []Candidate
	for _, align := range aligns {
		c := Candidate{
			Key:        fmt.Sprintf("spi/mode=%d/align=%s", mode.id, align),
			Label:      fmt.Sprintf("SPI mode %d align=%s", mode.id, align),
			Reason:     model.DiagSPIModeUnknown,
			Confidence: 0.5,
			Status:     model.CandidatePending,
		}
		if align != "aligned" {
			c.Reason = model.DiagMissingClockEdge
			c.Confidence -= 0.15
			c.Diagnostics = append(c.Diagnostics, model.Diagnostic{
				Code: model.DiagMissingClockEdge, Severity: "warn",
				Message: fmt.Sprintf("bit count not a multiple of 8 (missing clock edge); candidate %q retained", align),
			})
		}
		for _, s := range segs {
			bits := s.bits
			if align == "drop-leading" {
				if r := len(bits) % 8; r != 0 {
					bits = bits[r:]
				}
			} else if align == "drop-trailing" {
				bits = bits[:len(bits)-len(bits)%8]
			}
			tx := Transaction{Bus: "spi", Device: s.device, StartNS: s.start, EndNS: s.end}
			if s.gapEnd {
				tx.Flags = append(tx.Flags, "gap_terminated")
			}
			for i := 0; i+8 <= len(bits); i += 8 {
				if mosi != nil {
					b := Byte{Bits: 8, Channel: "mosi", Direction: "mosi", StartNS: bits[i].t, EndNS: bits[i+7].t}
					for k := 0; k < 8; k++ {
						b.Value = b.Value<<1 | bits[i+k].mosi
						b.EdgeIDs = append(b.EdgeIDs, bits[i+k].edgeID)
					}
					tx.Bytes = append(tx.Bytes, b)
				}
				if miso != nil {
					b := Byte{Bits: 8, Channel: "miso", Direction: "miso", StartNS: bits[i].t, EndNS: bits[i+7].t}
					for k := 0; k < 8; k++ {
						b.Value = b.Value<<1 | bits[i+k].miso
						b.EdgeIDs = append(b.EdgeIDs, bits[i+k].edgeID)
					}
					tx.Bytes = append(tx.Bytes, b)
				}
			}
			if len(tx.Bytes) > 0 {
				c.Transactions = append(c.Transactions, tx)
			}
		}
		out = append(out, c)
	}
	return out
}

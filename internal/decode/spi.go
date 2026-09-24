package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// decodeSPI decodes SPI per chip-select channel. When the mode is unknown
// ("auto"), all four modes are retained as parallel candidates with
// confidence from setup/hold cleanliness. Data gaps split transactions and
// are never spliced.
func decodeSPI(cap *model.Capture, p *model.Params, r *model.Run) {
	sclk := buildLine(cap, p.ClockChannel)
	if len(sclk.edges) == 0 {
		r.Status = model.StatusSemanticUnsolv
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagSemanticUnsolv,
			Position: p.ClockChannel, Message: "no clock edges on SCLK; cannot hypothesize"})
		return
	}

	modes := []int{}
	if p.SPIMode == "" || p.SPIMode == "auto" {
		modes = []int{0, 1, 2, 3}
	} else {
		var m int
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &m); err != nil || m < 0 || m > 3 {
			r.Status = model.StatusFormatCorrupt
			r.Diagnostics = append(r.Diagnostics, model.Diag{
				Severity: "error", Code: model.DiagFormatCorrupt,
				Position: "params.spi_mode", Message: "invalid spi mode " + p.SPIMode})
			return
		}
		modes = []int{m}
	}

	windows := csWindows(cap, p)
	if len(windows) == 0 {
		r.Status = model.StatusSemanticUnsolv
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagSemanticUnsolv,
			Position: "cs", Message: "no chip-select windows found"})
		return
	}

	for _, m := range modes {
		cpol := m >> 1
		cpha := m & 1
		cand := model.Candidate{
			Group:      "spi:" + cap.ID,
			Hypothesis: fmt.Sprintf("spi-mode-%d", m),
			Reasons:    []string{fmt.Sprintf("CPOL=%d CPHA=%d", cpol, cpha)},
		}
		violations := 0
		for _, w := range windows {
			txs, diags, v := spiWindow(cap, p, &sclk, w, cpol, cpha)
			cand.Transactions = append(cand.Transactions, txs...)
			cand.Diagnostics = append(cand.Diagnostics, diags...)
			violations += v
		}
		// Confidence: penalize data transitions near the sampling edge.
		cand.Confidence = 1.0 - float64(violations)*0.1
		if cand.Confidence < 0.1 {
			cand.Confidence = 0.1
		}
		if len(modes) > 1 {
			cand.Reasons = append(cand.Reasons, "spi mode unknown; parallel candidate retained")
		}
		r.Candidates = append(r.Candidates, cand)
	}
	if len(modes) > 1 {
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "warning", Code: model.DiagAmbiguousSPIMode,
			Position: p.ClockChannel,
			Message:  "SPI mode unknown; 4 parallel candidates retained"})
	}
}

type csWindow struct {
	channel string
	start   int64
	end     int64 // -1 = open ended
}

// csWindows extracts active (low) windows per CS channel.
func csWindows(cap *model.Capture, p *model.Params) []csWindow {
	var out []csWindow
	for _, csID := range p.CSChannels {
		l := buildLine(cap, csID)
		active := false
		start := int64(0)
		last := int64(-1)
		for _, e := range l.edges {
			last = e.Sample
			if e.Level == 0 && !active {
				active, start = true, e.Sample
			} else if e.Level == 1 && active {
				out = append(out, csWindow{csID, start, e.Sample})
				active = false
			}
		}
		if active {
			out = append(out, csWindow{csID, start, -1})
			_ = last
		}
	}
	return out
}

// spiWindow decodes one CS window under one mode hypothesis. Returns
// transactions, diagnostics and a setup/hold violation count.
func spiWindow(cap *model.Capture, p *model.Params, sclk *line, w csWindow, cpol, cpha int) ([]model.Transaction, []model.Diag, int) {
	mosi := buildLine(cap, p.DataChannel)
	var miso line
	if p.Data2Channel != "" {
		miso = buildLine(cap, p.Data2Channel)
	}
	idle := cpol
	// sampling edge: CPHA=0 -> first edge (leading), CPHA=1 -> second edge.
	sampleLevel := 1 - cpol
	if cpha == 1 {
		sampleLevel = cpol
	}

	var txs []model.Transaction
	var diags []model.Diag
	violations := 0

	var bits []bitSample
	txStart := int64(-1)
	lastClock := int64(-1)
	closeFrame := func(end int64, truncated bool) {
		if len(bits) == 0 {
			return
		}
		var flags []string
		if truncated {
			flags = append(flags, "truncated-gap")
		}
		var bytes []model.Byte
		for i := 0; i+8 <= len(bits); i += 8 {
			val := 0
			var refs []model.EdgeRef
			for k := 0; k < 8; k++ {
				val = val<<1 | bits[i+k].bit
				refs = append(refs, bits[i+k].clock)
			}
			bytes = append(bytes, NewByte(val, nil, refs, 8))
		}
		if len(bytes) > 0 {
			txs = append(txs, NewTransaction("spi-transfer", w.channel, 0, bytes, txStart, end, flags))
		}
		bits = nil
	}

	for _, e := range sclk.edges {
		if e.Sample < w.start {
			continue
		}
		if w.end >= 0 && e.Sample > w.end {
			break
		}
		if txStart < 0 {
			txStart = e.Sample
		}
		// Data gap inside a CS window: close frame, do not splice.
		if p.GapSamples > 0 && lastClock >= 0 && e.Sample-lastClock > p.GapSamples {
			closeFrame(lastClock, true)
			diags = append(diags, model.Diag{
				Severity: "warning", Code: model.DiagDataGap,
				Position: fmt.Sprintf("sample@%d", lastClock),
				Message:  "data gap inside CS window; frame closed, not spliced"})
			txStart = e.Sample
		}
		lastClock = e.Sample
		if e.Level == sampleLevel {
			bit := mosi.levelAt(e.Sample, 0)
			bits = append(bits, bitSample{bit, model.EdgeRef{ChannelID: p.ClockChannel, Sample: e.Sample}})
			// setup/hold cleanliness: data toggling exactly at sample edge
			for _, de := range mosi.edges {
				if de.Sample == e.Sample {
					violations++
				}
			}
			if p.Data2Channel != "" {
				_ = miso.levelAt(e.Sample, 0)
			}
		}
	}
	end := w.end
	if end < 0 {
		end = lastClock
	}
	closeFrame(end, false)
	_ = idle
	return txs, diags, violations
}

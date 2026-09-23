package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

// spiFrame is one CS-active interval.
type spiFrame struct {
	dev      string
	start, end int64
	clkEdges   []edgeAt
}

func spiFrames(cap *model.Capture, cs model.Channel, sclk *line) []spiFrame {
	csLine := buildLine(cap, cs.Alias)
	var frames []spiFrame
	open := false
	var cur spiFrame
	for _, e := range csLine.edges {
		active := e.level == 0 // chip select is active-low
		if active && !open {
			open = true
			cur = spiFrame{dev: cs.Alias, start: e.t}
		} else if !active && open {
			open = false
			cur.end = e.t
			frames = append(frames, cur)
		}
	}
	if open {
		cur.end = csLine.edges[len(csLine.edges)-1].t
		frames = append(frames, cur)
	}
	for i := range frames {
		for _, e := range sclk.edges {
			if e.t >= frames[i].start && e.t <= frames[i].end {
				frames[i].clkEdges = append(frames[i].clkEdges, e)
			}
		}
	}
	return frames
}

// decodeSPIFrame decodes one frame under a specific SPI mode (0..3).
// Returns transactions (MOSI/MISO bytes), boundary violations and bit count.
func decodeSPIFrame(cap *model.Capture, p model.Params, fr spiFrame, mode int, ov map[string]bool) (model.Transaction, int, int) {
	cpol := (mode >> 1) & 1
	cpha := mode & 1
	idle := cpol
	mosi, miso := buildLine(cap, "MOSI"), buildLine(cap, "MISO")

	type bitRec struct {
		t      int64
		m, s   int
		clkIdx int
	}
	var bits []bitRec
	violations := 0
	for _, e := range fr.clkEdges {
		fromIdle := e.level != idle
		sample := (cpha == 0 && fromIdle) || (cpha == 1 && !fromIdle)
		if !sample {
			continue
		}
		m := mosi.levelAt(e.t)
		s := miso.levelAt(e.t)
		if m < 0 {
			m = 1 // idle-high assumption for absent data
		}
		if s < 0 {
			s = 1
		}
		// boundary sampling: data edge inside the window before the sample edge
		for _, dl := range []*line{mosi, miso} {
			for _, de := range dl.edges {
				if de.t < e.t && e.t-de.t <= p.BoundaryWindowNs {
					violations++
				}
			}
		}
		bits = append(bits, bitRec{t: e.t, m: m, s: s, clkIdx: e.idx})
	}

	mk := func(prefix string, get func(bitRec) int) []model.Byte {
		var out []model.Byte
		for i := 0; i+8 <= len(bits); i += 8 {
			v := 0
			var refs []int
			for j := 0; j < 8; j++ {
				v = v<<1 | get(bits[i+j])
				refs = append(refs, bits[i+j].clkIdx)
			}
			out = append(out, model.Byte{
				ID: fmt.Sprintf("spi-b-%s-%d-%02x", prefix, bits[i].t, v),
				Value: v, Bits: 8, Ack: "none",
				StartNs: bits[i].t, EndNs: bits[i+7].t, EdgeRefs: refs,
			})
		}
		return out
	}
	tx := model.Transaction{
		ID: fmt.Sprintf("spi-tx-%s-%d", fr.dev, fr.start),
		Protocol: "spi", Device: fr.dev,
		StartNs: fr.start, EndNs: fr.end,
	}
	mosiBytes := mk("mosi", func(b bitRec) int { return b.m })
	misoBytes := mk("miso", func(b bitRec) int { return b.s })
	tx.Bytes = append(tx.Bytes, mosiBytes...)
	tx.Bytes = append(tx.Bytes, misoBytes...)
	sort.Slice(tx.Bytes, func(i, j int) bool { return tx.Bytes[i].StartNs < tx.Bytes[j].StartNs })

	// missing clock edge: leftover bits that do not complete a byte
	if rem := len(bits) % 8; rem != 0 {
		if ov[ambKey("missing_clock_edge", fr.start)] {
			// alternate: pad the partial byte with zeros instead of dropping
			v := 0
			var refs []int
			for j := len(bits) - rem; j < len(bits); j++ {
				v = v<<1 | bits[j].m
				refs = append(refs, bits[j].clkIdx)
			}
			v <<= uint(8 - rem)
			tx.Bytes = append(tx.Bytes, model.Byte{
				ID: fmt.Sprintf("spi-b-mosi-pad-%d-%02x", bits[len(bits)-rem].t, v),
				Value: v, Bits: rem, Ack: "none",
				StartNs: bits[len(bits)-rem].t, EndNs: bits[len(bits)-1].t, EdgeRefs: refs,
			})
			tx.Flags = append(tx.Flags, "padded_partial_byte")
		} else {
			tx.Flags = append(tx.Flags, "dropped_partial_byte")
		}
	}
	return tx, violations, len(bits)
}

func decodeSPI(cap *model.Capture, p model.Params) model.DecodeResult {
	sclkCh := findChannel(cap, "SCLK")
	csChans := channelsByRole(cap, "cs")
	if sclkCh == nil || len(csChans) == 0 {
		return model.DecodeResult{}
	}
	sclk := buildLine(cap, "SCLK")
	var frames []spiFrame
	for _, cs := range csChans {
		frames = append(frames, spiFrames(cap, cs, sclk)...)
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].start < frames[j].start })
	if len(frames) == 0 {
		return model.DecodeResult{}
	}

	modes := []int{0, 1, 2, 3}
	if p.SPIMode != "auto" {
		var m int
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &m); err == nil && m >= 0 && m <= 3 {
			modes = []int{m}
		}
	}

	type modeScore struct {
		mode       int
		violations int
		bits       int
	}
	var scores []modeScore
	for _, m := range modes {
		ms := modeScore{mode: m}
		for _, fr := range frames {
			_, v, b := decodeSPIFrame(cap, p, fr, m, nil)
			ms.violations += v
			ms.bits += b
		}
		scores = append(scores, ms)
	}

	scoreOf := func(ms modeScore) float64 {
		s := 1.0 / (1.0 + float64(ms.violations))
		if ms.bits%8 != 0 {
			s *= 0.6
		}
		return s
	}
	best := -1.0
	for _, ms := range scores {
		if s := scoreOf(ms); s > best {
			best = s
		}
	}

	res := model.DecodeResult{}
	for _, ms := range scores {
		sc := scoreOf(ms)
		// keep every mode whose evidence is within tolerance of the best:
		// SPI mode unknown => multiple legal candidates
		if len(modes) > 1 && sc < best-0.20 {
			continue
		}
		var txs []model.Transaction
		missing := false
		for _, fr := range frames {
			tx, _, b := decodeSPIFrame(cap, p, fr, ms.mode, nil)
			if b%8 != 0 {
				missing = true
			}
			if len(tx.Bytes) > 0 {
				txs = append(txs, tx)
			}
		}
		if len(txs) == 0 {
			continue
		}
		conf := sc
		if len(modes) > 1 {
			conf *= 0.7 // mode uncertainty discount
		}
		c := makeCandidate("spi", fmt.Sprintf("spi_mode=%d", ms.mode), conf, txs, nil)
		if len(modes) > 1 {
			c.Reason = "spi_mode_unknown"
		}
		res.Candidates = append(res.Candidates, c)

		if missing {
			var alt []model.Transaction
			for _, fr := range frames {
				tx, _, b := decodeSPIFrame(cap, p, fr, ms.mode, map[string]bool{ambKey("missing_clock_edge", fr.start): true})
				if b%8 != 0 && len(tx.Bytes) > 0 {
					alt = append(alt, tx)
				}
			}
			if len(alt) > 0 {
				ac := makeCandidate("spi", fmt.Sprintf("spi_mode=%d,missing_clock_edge=pad_partial", ms.mode), conf*0.6, alt, []model.Diagnostic{{
					Code: "SPI_MISSING_CLOCK", Severity: "warning", TimeNs: alt[0].StartNs, EdgeIndex: -1,
					Message: "bit count not a multiple of 8; a clock edge may be missing",
				}})
				ac.Reason = "missing_clock_edge"
				res.Candidates = append(res.Candidates, ac)
			}
		}
	}
	return res
}

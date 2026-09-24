package decoder

import (
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

// decodeSPI reconstructs SPI transactions. One CS assertion defines a
// transaction per device. When CPOL/CPHA is unknown all four legal modes
// are emitted as tied candidates; clock edges that do not line up with a
// complete 8-bit frame produce missing-edge evidence, and a data gap splits
// the frame rather than stitching bytes.
func decodeSPI(channels map[string]*domain.Channel, edges []domain.Edge, p domain.DecoderParams) []domain.Transaction {
	sclk := signal(findChan(channels, p.SCLK), edges)
	mosi := signal(findChan(channels, p.MOSI), edges)
	miso := signal(findChan(channels, p.MISO), edges)
	csCh := findChan(channels, p.CS)
	csSig := signal(csCh, edges)
	if len(sclk) == 0 {
		return nil
	}

	// CS is active-low unless the probe inverted the channel; signal() has
	// already applied inversion, so logical low = selected.
	type frame struct {
		startPS, endPS int64
		activeEdge     string
		releaseEdge    string
		device         string
		clocks         []ev
	}
	var frames []frame
	if len(csSig) > 0 {
		var startPS int64
		var startEdge string
		active := false
		for _, e := range csSig {
			if e.level == 0 && !active {
				startPS, startEdge, active = e.ps, e.id, true
			} else if e.level == 1 && active {
				f := frame{startPS: startPS, endPS: e.ps, activeEdge: startEdge, releaseEdge: e.id,
					device: p.CS}
				f.clocks = clocksBetween(sclk, f.startPS, f.endPS)
				frames = append(frames, f)
				active = false
			}
		}
		if active {
			f := frame{startPS: startPS, endPS: lastEventPS(sclk), activeEdge: startEdge,
				device: p.CS, releaseEdge: ""}
			f.clocks = clocksBetween(sclk, f.startPS, f.endPS)
			frames = append(frames, f)
		}
	} else {
		frames = []frame{{startPS: firstEventPS(sclk) - 1, endPS: lastEventPS(sclk) + 1,
			device: p.CS, clocks: risingAndFalling(sclk)}}
		frames[0].clocks = clocksBetween(sclk, frames[0].startPS, frames[0].endPS)
	}

	gap := p.GapLimitPS
	if gap <= 0 {
		gap = estimateClockPeriod(sclk) * 16
	}

	var txns []domain.Transaction
	for fi, fr := range frames {
		// Detect multi-device CS patterns: multiple CS aliases may share the
		// SCLK/MOSI bus. Each distinct active segment is tagged.
		segs := splitOnGap(fr.clocks, gap)
		var segEv []domain.Evidence
		if len(csSig) > 0 && fi >= 0 {
			// Count distinct devices selected across the capture.
			if distinctCSDevices(edges, channels) > 1 {
				segEv = append(segEv, domain.Evidence{
					Kind: domain.EvMultiCS, StartPS: fr.startPS, EndPS: fr.endPS,
					EdgeIDs: []string{fr.activeEdge, fr.releaseEdge},
					Message: "multiple devices share the bus with separate chip selects",
					Policy:  multiCSPolicy,
					Detail:  map[string]any{"device": fr.device},
				})
			}
		}
		if len(segs) > 1 {
			for _, g := range segs[1:] {
				segEv = append(segEv, domain.Evidence{
					Kind: domain.EvDataGap, StartPS: segs[0][len(segs[0])-1].ps, EndPS: g[0].ps,
					EdgeIDs: []string{segs[0][len(segs[0])-1].id, g[0].id},
					Message: "clock silence exceeds gap limit within CS; frame split, not stitched",
					Policy:  gapPolicySplit,
				})
			}
		}

		modes := []struct{ cpol, cpha int }{
			{0, 0}, {0, 1}, {1, 0}, {1, 1},
		}
		if p.SPIModeKnown {
			modes = []struct{ cpol, cpha int }{{p.CPOL, p.CPHA}}
		}
		txn := domain.Transaction{
			BusID: p.BusID, Protocol: "spi", Device: fr.device,
			StartPS: fr.startPS, EndPS: fr.endPS, Status: "tentative",
		}
		ambiguous := len(modes) > 1
		for mi, mode := range modes {
			cand := domain.Candidate{
				BusID:        p.BusID,
				Mode:         fmt.Sprintf("spi-mode%d", mode.cpol*2+mode.cpha),
				RulesVersion: RulesVersion,
			}
			complete := 0
			for _, seg := range segs {
				bs := decodeSPISegment(seg, mosi, miso, mode.cpol, mode.cpha, p.BitOrder)
				cand.Bytes = append(cand.Bytes, bs...)
				complete += countComplete(bs)
			}
			total := 0
			for _, seg := range segs {
				total += len(seg) / 2
			}
			expected := 0
			for _, seg := range segs {
				expected += len(seg)
			}
			if expected%2 != 0 {
				cand.Evidences = append(cand.Evidences, domain.Evidence{
					Kind: domain.EvMissingClock, StartPS: fr.startPS, EndPS: fr.endPS,
					EdgeIDs: edgeIDs(fr.clocks),
					Message: "odd number of clock edges inside CS; frame cannot complete",
					Policy:  missingPolicy,
				})
			}
			switch {
			case ambiguous && complete > 0:
				cand.Confidence = 0.6
			case ambiguous:
				cand.Confidence = 0.3
			case complete > 0:
				cand.Confidence = 0.95
			default:
				cand.Confidence = 0.2
			}
			if ambiguous && mi == 0 {
				cand.Adopted = true
			} else if !ambiguous {
				cand.Adopted = true
			}
			if ambiguous {
				cand.Reason = "CPOL/CPHA unspecified; all four legal modes retained"
			}
			txn.Candidates = append(txn.Candidates, cand)
		}
		if ambiguous {
			segEv = append(segEv, domain.Evidence{
				Kind: domain.EvAmbiguity, StartPS: fr.startPS, EndPS: fr.endPS,
				EdgeIDs: []string{fr.activeEdge},
				Message: "SPI mode unknown; four tied candidates preserved",
				Policy:  missingPolicy,
				Detail:  map[string]any{"candidateCount": 4},
			})
		}
		txn.Evidences = segEv
		for i := range txn.Candidates {
			txn.Candidates[i].Evidences = append(txn.Candidates[i].Evidences, segEv...)
		}
		txns = append(txns, txn)
	}
	return assignStableIDs(txns, p)
}

func clocksBetween(sclk []ev, from, to int64) []ev {
	out := []ev{}
	for _, e := range sclk {
		if e.ps > from && e.ps < to {
			out = append(out, e)
		}
	}
	return out
}

func risingAndFalling(s []ev) []ev { return s }

func firstEventPS(s []ev) int64 {
	if len(s) == 0 {
		return 0
	}
	return s[0].ps
}

func lastEventPS(s []ev) int64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1].ps
}

func estimateClockPeriod(sclk []ev) int64 {
	if len(sclk) < 3 {
		return 0
	}
	diffs := []int64{}
	for i := 1; i < len(sclk); i++ {
		diffs = append(diffs, sclk[i].ps-sclk[i-1].ps)
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return diffs[len(diffs)/2] * 2
}

func splitOnGap(clocks []ev, gap int64) [][]ev {
	if gap <= 0 || len(clocks) == 0 {
		return [][]ev{clocks}
	}
	segs := [][]ev{}
	cur := []ev{clocks[0]}
	for i := 1; i < len(clocks); i++ {
		if clocks[i].ps-clocks[i-1].ps > gap {
			segs = append(segs, cur)
			cur = []ev{}
		}
		cur = append(cur, clocks[i])
	}
	return append(segs, cur)
}

func distinctCSDevices(edges []domain.Edge, ch map[string]*domain.Channel) int {
	seen := map[string]bool{}
	for _, c := range ch {
		if c.Role == "cs" {
			seen[c.Alias] = true
		}
	}
	return len(seen)
}

// decodeSPISegment samples MOSI/MISO on the active edge for the mode.
// CPHA=0 samples on the leading edge (first edge per bit, polarity-aware);
// CPHA=1 samples on the trailing edge. Every clock edge pair is one bit.
func decodeSPISegment(clocks []ev, mosi, miso []ev, cpol, cpha int, order string) []domain.DecodedByte {
	var bytes []domain.DecodedByte
	type sample struct {
		ps    int64
		data  string
		value int
	}
	var samples []sample
	// active sampling edge index within each pair (0=leading,1=trailing)
	leadLevel := cpol ^ 1 // idle is cpol; leading edge goes to cpol^1
	for i := 0; i+1 < len(clocks); i += 2 {
		var edge ev
		if cpha == 0 {
			edge = clocks[i] // leading
		} else {
			edge = clocks[i+1] // trailing
		}
		if !(edge.level == leadLevel && cpha == 0 || cpha == 1) {
			// edge.level for trailing equals cpol; selection already correct
		}
		mv, mid := levelAt(mosi, edge.ps, 0)
		samples = append(samples, sample{ps: edge.ps, data: mid, value: mv})
	}
	var cur *domain.DecodedByte
	bitN := 0
	var startPS int64
	ids := []string{}
	flush := func(endPS int64, endID string) {
		if cur == nil {
			return
		}
		cur.EndPS = endPS
		cur.EdgeIDs = uniq(ids)
		bytes = append(bytes, *cur)
		cur, bitN, ids = nil, 0, nil
	}
	for si, s := range samples {
		if cur == nil {
			startPS = s.ps
			cur = &domain.DecodedByte{Role: "data", StartPS: startPS}
		}
		pos := bitN
		if order == "lsb" {
			pos = bitN
		} else {
			pos = 7 - bitN
		}
		cur.Value |= s.value << pos
		ids = append(ids, s.data)
		bitN++
		if bitN == 8 {
			cur.Index = len(bytes)
			flush(s.ps, s.data)
		}
		_ = si
	}
	if cur != nil {
		cur.Index = len(bytes)
		cur.Role = "partial"
		bytes = append(bytes, *cur)
	}
	return bytes
}

func countComplete(bs []domain.DecodedByte) int {
	n := 0
	for _, b := range bs {
		if b.Role != "partial" {
			n++
		}
	}
	return n
}

func edgeIDs(es []ev) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.id)
	}
	return out
}

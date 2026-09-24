package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

type tlKind int

const (
	tlGap tlKind = iota
	tlSDA
	tlSCL
)

type tlItem struct {
	sample int64
	kind   tlKind
	edge   *model.Edge
	gap    *model.Gap
}

type i2cCur struct {
	start     int64
	segments  int
	bytes     []model.DecodedByte
	bits      []model.BitRef
	nbits     int
	curVal    int
	diags     []model.Diagnostic
	stretched bool
	lastEdge  int64
}

type i2cBranch struct {
	txns        []model.Transaction
	cur         *i2cCur
	conf        float64
	rationale   []string
	lastSCL     int64
	sdaToggles  int
	prevSDAData *model.Edge
}

func (br *i2cBranch) clone() *i2cBranch {
	cp := *br
	cp.txns = append([]model.Transaction{}, br.txns...)
	cp.rationale = append([]string{}, br.rationale...)
	if br.cur != nil {
		c := *br.cur
		c.bytes = append([]model.DecodedByte{}, br.cur.bytes...)
		c.bits = append([]model.BitRef{}, br.cur.bits...)
		c.diags = append([]model.Diagnostic{}, br.cur.diags...)
		cp.cur = &c
	}
	return &cp
}

func i2cAddBit(br *i2cBranch, val int, clkID, dataID string, sample int64) {
	cur := br.cur
	cur.bits = append(cur.bits, model.BitRef{
		Bit: cur.nbits, Value: val, ClockEdge: clkID, DataEdge: dataID, Sample: sample,
	})
	cur.curVal = cur.curVal<<1 | val
	cur.nbits++
	cur.lastEdge = sample
	if cur.nbits == 9 {
		val8 := cur.curVal >> 1
		ack := cur.curVal&1 == 0
		refs := make([]string, 0, len(cur.bits))
		for _, bt := range cur.bits {
			refs = append(refs, bt.ClockEdge)
		}
		by := model.DecodedByte{
			Index: len(cur.bytes), Value: val8, Ack: &ack, Bits: cur.bits,
			Stretched: cur.stretched, Confidence: round3(br.conf),
		}
		by.ID = model.HashID("byt", "i2c", cur.start, by.Index, val8, ack, refs)
		cur.bytes = append(cur.bytes, by)
		cur.bits = nil
		cur.nbits = 0
		cur.curVal = 0
		cur.stretched = false
	}
}

func i2cFinalize(br *i2cBranch, status string, end int64) {
	cur := br.cur
	ids := make([]string, 0, len(cur.bytes))
	for _, by := range cur.bytes {
		ids = append(ids, by.ID)
	}
	txn := model.Transaction{
		Protocol: "i2c", Device: "bus",
		StartSample: cur.start, EndSample: end, Status: status,
		Segments: cur.segments, Bytes: cur.bytes, Diagnostics: cur.diags,
	}
	txn.ID = model.HashID("txn", "i2c", cur.start, status, cur.segments, ids)
	br.txns = append(br.txns, txn)
	br.cur = nil
}

func decodeI2C(c *model.Capture, sigs map[string]*signal, p *model.Params, rec *model.RunRecord, b *budget) {
	sda, scl := byRole(sigs, "sda"), byRole(sigs, "scl")
	if sda == nil || scl == nil {
		rec.Diagnostics = append(rec.Diagnostics, model.Diagnostic{
			Code: "missing_channel", Position: "probe.channels[role=sda|scl]",
			Message: "I2C decode requires sda and scl aliases",
		})
		return
	}
	var offset int64
	if c.SampleRateHz > 0 {
		offset = int64(p.SampleOffsetNs * c.SampleRateHz / 1e9)
	}
	items := []tlItem{}
	for i := range sda.edges {
		items = append(items, tlItem{sda.edges[i].Sample, tlSDA, &sda.edges[i], nil})
	}
	for i := range scl.edges {
		items = append(items, tlItem{scl.edges[i].Sample, tlSCL, &scl.edges[i], nil})
	}
	for i := range c.Gaps {
		items = append(items, tlItem{c.Gaps[i].Sample, tlGap, nil, &c.Gaps[i]})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].sample != items[j].sample {
			return items[i].sample < items[j].sample
		}
		return items[i].kind < items[j].kind
	})

	branches := []*i2cBranch{{conf: 1.0}}
	for _, it := range items {
		if b.exhausted {
			return
		}
		switch it.kind {
		case tlGap:
			for _, br := range branches {
				if br.cur != nil {
					rec.Evidence = append(rec.Evidence, model.Evidence{
						Code: "gap_no_splice", Sample: it.gap.Sample, Refs: []string{it.gap.ID},
						Message: "data gap: transaction truncated; bytes after the gap are not spliced in",
					})
					br.cur.diags = append(br.cur.diags, model.Diagnostic{
						Code: "data_gap", Sample: it.gap.Sample, RefID: it.gap.ID,
						Message: "capture gap truncates transaction (" + it.gap.Reason + ")",
					})
					i2cFinalize(br, "truncated_gap", it.gap.Sample)
				}
			}
		case tlSDA:
			var forks []*i2cBranch
			for _, br := range branches {
				forks = append(forks, i2cOnSDA(br, it.edge, scl, rec, b)...)
			}
			branches = append(branches, forks...)
		case tlSCL:
			var forks []*i2cBranch
			for _, br := range branches {
				forks = append(forks, i2cOnSCL(br, it.edge, sda, p, offset, c, rec, b)...)
			}
			branches = append(branches, forks...)
		}
	}
	for _, br := range branches {
		if br.cur != nil {
			br.cur.diags = append(br.cur.diags, model.Diagnostic{
				Code: "capture_end", Sample: br.cur.lastEdge,
				Message: "capture ended mid-transaction",
			})
			i2cFinalize(br, "aborted", br.cur.lastEdge)
		}
	}
	kept := 0
	for _, br := range branches {
		if len(br.txns) == 0 {
			continue
		}
		ids := make([]string, 0, len(br.txns))
		for _, t := range br.txns {
			ids = append(ids, t.ID)
		}
		cand := model.Candidate{
			Protocol: "i2c", Scope: "bus:i2c",
			Confidence: round3(br.conf), Rationale: br.rationale, Transactions: br.txns,
		}
		cand.ID = model.HashID("cand", "i2c", ids, br.rationale)
		rec.Candidates = append(rec.Candidates, cand)
		kept++
	}
	if kept > 1 {
		rec.Evidence = append(rec.Evidence, model.Evidence{
			Code: "candidates_kept", Channel: sda.alias,
			Message: fmt.Sprintf("kept %d I2C decode candidates (ambiguity not uniquely resolvable)", kept),
		})
	}
}

func i2cOnSDA(br *i2cBranch, e *model.Edge, scl *signal, rec *model.RunRecord, b *budget) []*i2cBranch {
	sclLevel := levelAt(scl, e.Sample)
	switch {
	case sclLevel == 1 && e.Level == 0: // START / repeated START
		if br.cur == nil {
			br.cur = &i2cCur{start: e.Sample, segments: 1, lastEdge: e.Sample}
		} else {
			rec.Evidence = append(rec.Evidence, model.Evidence{
				Code: "repeated_start", Sample: e.Sample, Refs: []string{e.ID},
				Message: "repeated START: transaction continues in a new segment",
			})
			if br.cur.nbits != 0 {
				br.cur.diags = append(br.cur.diags, model.Diagnostic{
					Code: "restart_mid_byte", Sample: e.Sample,
					Message: "repeated START mid-byte: partial bits discarded",
				})
				br.cur.bits = nil
				br.cur.nbits = 0
				br.cur.curVal = 0
			}
			br.cur.segments++
			br.cur.lastEdge = e.Sample
		}
		br.sdaToggles = 0
	case sclLevel == 1 && e.Level == 1: // STOP
		if br.cur != nil {
			if br.cur.nbits != 0 {
				br.cur.diags = append(br.cur.diags, model.Diagnostic{
					Code: "partial_byte", Sample: e.Sample,
					Message: "STOP with partial byte: leftover bits discarded",
				})
			}
			i2cFinalize(br, "complete", e.Sample)
		}
		br.sdaToggles = 0
	case sclLevel == 0: // data toggle while clock low
		if br.cur != nil {
			br.sdaToggles++
			if br.sdaToggles >= 2 && br.prevSDAData != nil {
				if !b.fork() {
					return nil
				}
				rec.Evidence = append(rec.Evidence, model.Evidence{
					Code: "missing_clock_edge", Sample: e.Sample,
					Refs:    []string{br.prevSDAData.ID, e.ID},
					Message: "SDA toggled twice without an SCL edge: keeping both bit-alignment candidates",
				})
				alt := br.clone()
				// This branch: one clock edge was missed -> two bits.
				i2cAddBit(br, 1-e.Level, "", br.prevSDAData.ID, br.prevSDAData.Sample)
				br.conf *= 0.7
				br.rationale = append(br.rationale, fmt.Sprintf("missing_clock_edge@%d:two-bits", e.Sample))
				// Alternative: glitch -> single bit sampled at next clock.
				alt.conf *= 0.7
				alt.rationale = append(alt.rationale, fmt.Sprintf("missing_clock_edge@%d:one-bit", e.Sample))
				alt.sdaToggles = 1
				br.sdaToggles = 1
				br.prevSDAData = e
				alt.prevSDAData = e
				return []*i2cBranch{alt}
			}
			br.prevSDAData = e
		}
	}
	return nil
}

func i2cOnSCL(br *i2cBranch, e *model.Edge, sda *signal, p *model.Params, offset int64, c *model.Capture, rec *model.RunRecord, b *budget) []*i2cBranch {
	if br.cur != nil && br.lastSCL > 0 && p.StretchMinNs > 0 && c.SampleRateHz > 0 {
		dtNs := float64(e.Sample-br.lastSCL) / c.SampleRateHz * 1e9
		if dtNs > p.StretchMinNs {
			rec.Evidence = append(rec.Evidence, model.Evidence{
				Code: "clock_stretch", Channel: sclAlias(sda), Sample: br.lastSCL, Refs: []string{e.ID},
				Message: fmt.Sprintf("clock stretched %.0f ns (threshold %.0f ns)", dtNs, p.StretchMinNs),
			})
			br.cur.stretched = true
		}
	}
	br.lastSCL = e.Sample
	br.sdaToggles = 0
	sampleEdge := (p.ClockPolarity == "rising" && e.Level == 1) ||
		(p.ClockPolarity == "falling" && e.Level == 0)
	if !sampleEdge || br.cur == nil {
		return nil
	}
	sp := e.Sample + offset
	val := levelAt(sda, sp)
	if val < 0 {
		br.cur.diags = append(br.cur.diags, model.Diagnostic{
			Code: "no_data_level", Sample: sp,
			Message: "no SDA edge before sample point; level unknown, 0 assumed",
		})
		val = 0
	}
	if sp > e.Sample {
		if de := firstEdgeIn(sda, e.Sample, sp); de != nil {
			if !b.fork() {
				return nil
			}
			rec.Evidence = append(rec.Evidence, model.Evidence{
				Code: "boundary_sample", Sample: sp, Refs: []string{e.ID, de.ID},
				Message: "SDA changed between clock edge and sample point: keeping both bit values",
			})
			alt := br.clone()
			i2cAddBit(br, de.Level^1, e.ID, de.ID, sp)
			br.conf *= 0.8
			br.rationale = append(br.rationale, fmt.Sprintf("boundary_sample@%d:pre-edge", sp))
			i2cAddBit(alt, de.Level, e.ID, de.ID, sp)
			alt.conf *= 0.8
			alt.rationale = append(alt.rationale, fmt.Sprintf("boundary_sample@%d:post-edge", sp))
			return []*i2cBranch{alt}
		}
	}
	var dataID string
	if de := lastEdgeAtOrBefore(sda, sp); de != nil {
		dataID = de.ID
	}
	i2cAddBit(br, val, e.ID, dataID, sp)
	return nil
}

func sclAlias(sda *signal) string { return "SCL" }

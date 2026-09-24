package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// i2cBit is one sampled data bit with provenance.
type i2cBit struct {
	value  int
	edgeID string // clock edge that sampled it
	timeNS int64
}

// i2cFrame is a raw decoded transaction before byte assembly.
type i2cFrame struct {
	bits    []i2cBit
	startNS int64
	endNS   int64
	flags   []string
	noStart bool
}

// DecodeI2C decodes the I2C bus of a capture. Clock polarity, boundary
// sampling, repeated START, clock stretching, data gaps and missing clock
// edges are all handled explicitly; ambiguity produces retained candidates.
func DecodeI2C(m *model.Material, p model.Params) Result {
	res := Result{Kind: model.KindOK, Bus: "i2c"}
	scl := m.ChannelByRole(model.RoleSCL)
	sda := m.ChannelByRole(model.RoleSDA)
	if scl == nil || sda == nil {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "capture has no scl/sda channel pair",
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
	clks := clockEdges(m, *scl, p.ClockPolarity)
	if len(clks) == 0 {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error", Channel: scl.ID,
			Message: "no clock edges on scl; bus cannot be decoded",
		})
		return res
	}

	prefs := []string{"before"}
	if p.Boundary == "strict" {
		prefs = []string{"before", "after"}
	}
	var candidates []Candidate
	for _, pref := range prefs {
		frames, diags := i2cFrames(m, p, *scl, *sda, clks, pref)
		if len(frames) == 0 {
			continue
		}
		variants := assembleI2C(frames, pref)
		for _, v := range variants {
			v.Diagnostics = append(v.Diagnostics, diags...)
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "no START condition found; no transaction could be framed",
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

// i2cFrames runs the sampling state machine and returns raw frames.
func i2cFrames(m *model.Material, p model.Params, scl, sda model.Channel, clks []clockEdge, pref string) ([]i2cFrame, []model.Diagnostic) {
	sdaTl := newTimeline(m, sda)
	sclTl := newTimeline(m, scl)
	var diags []model.Diagnostic
	var frames []i2cFrame
	var cur *i2cFrame
	open := false
	lastClkNS := int64(-1)

	// SDA edges sorted for START/STOP detection.
	sdaEdges := m.EdgesFor(sda.ID)

	closeTxn := func(flag string) {
		if cur != nil && len(cur.bits) > 0 {
			if flag != "" {
				cur.flags = append(cur.flags, flag)
			}
			frames = append(frames, *cur)
		}
		cur = nil
		open = false
	}

	// Merge clock edges and SDA edges into one time-ordered stream.
	type ev struct {
		t     int64
		isClk bool
		edge  model.Edge
	}
	var stream []ev
	for _, c := range clks {
		stream = append(stream, ev{t: c.edge.TimeNS, isClk: true, edge: c.edge})
	}
	for _, e := range sdaEdges {
		if e.TimeOK {
			stream = append(stream, ev{t: e.TimeNS, edge: e})
		}
	}
	// Clock events first at equal timestamps so a START/STOP is evaluated
	// against the clock level at that instant.
	for i := 1; i < len(stream); i++ {
		for j := i; j > 0 && (stream[j].t < stream[j-1].t || (stream[j].t == stream[j-1].t && stream[j].isClk && !stream[j-1].isClk)); j-- {
			stream[j], stream[j-1] = stream[j-1], stream[j]
		}
	}

	for _, e := range stream {
		if e.isClk {
			if lastClkNS >= 0 {
				delta := e.t - lastClkNS
				if delta > p.GapNS && open {
					closeTxn("gap_terminated")
					diags = append(diags, model.Diagnostic{
						Code: model.DiagDataGap, Severity: "warn", Channel: scl.ID,
						EdgeID: e.edge.ID, TimeNS: e.t,
						Message: fmt.Sprintf("clock idle %s exceeds gap threshold %s: transaction closed, bytes are not stitched across the gap", model.FormatNS(delta), model.FormatNS(p.GapNS)),
					})
				} else if delta > p.StretchNS && open {
					diags = append(diags, model.Diagnostic{
						Code: model.DiagClockStretch, Severity: "info", Channel: scl.ID,
						EdgeID: e.edge.ID, TimeNS: lastClkNS,
						Message: fmt.Sprintf("clock held low %s: clock stretching tolerated", model.FormatNS(delta)),
					})
					if cur != nil && !contains(cur.flags, "clock_stretch") {
						cur.flags = append(cur.flags, "clock_stretch")
					}
				}
			}
			lastClkNS = e.t
			if !open {
				// Bits outside any START are collected into a no-start frame
				// only after a gap resume; ignore otherwise.
				continue
			}
			var bit int
			if pref == "after" {
				bit = sdaTl.levelAt(e.t)
			} else {
				bit = sdaTl.levelJustBefore(e.t)
			}
			if hasEdgeAt(sdaEdges, e.t) {
				diags = append(diags, model.Diagnostic{
					Code: model.DiagBoundaryAmbiguous, Severity: "warn", Channel: sda.ID,
					EdgeID: e.edge.ID, TimeNS: e.t,
					Message: fmt.Sprintf("sda changes exactly at the sampling clock edge; sampled %s the edge", pref),
				})
			}
			cur.bits = append(cur.bits, i2cBit{value: bit, edgeID: e.edge.ID, timeNS: e.t})
			cur.endNS = e.t
			continue
		}
		// SDA transition: START = falling while SCL high, STOP = rising while SCL high.
		logical := e.edge.Level
		if sda.Inverted {
			logical = 1 - logical
		}
		sclHigh := sclTl.levelAt(e.t) == 1
		if !sclHigh {
			continue
		}
		if logical == 0 { // falling while SCL high: START
			if open {
				diags = append(diags, model.Diagnostic{
					Code: model.DiagRepeatedStart, Severity: "info", Channel: sda.ID,
					EdgeID: e.edge.ID, TimeNS: e.t,
					Message: "repeated START: new address phase inside the same transaction",
				})
				if cur != nil && !contains(cur.flags, "repeated_start") {
					cur.flags = append(cur.flags, "repeated_start")
				}
				continue
			}
			cur = &i2cFrame{startNS: e.t, endNS: e.t}
			open = true
		} else { // rising while SCL high: STOP
			if open {
				closeTxn("")
			}
		}
	}
	if open {
		closeTxn("")
	}
	return frames, diags
}

// assembleI2C groups frame bits into 9-bit byte+ack units. When the bit
// count is not a multiple of 9 (a missing clock edge), both legal alignments
// are retained as separate candidates instead of guessing silently.
func assembleI2C(frames []i2cFrame, pref string) []Candidate {
	type variant struct {
		align string
		drop  int // leading bits to drop
	}
	needsChoice := false
	for _, f := range frames {
		if len(f.bits)%9 != 0 {
			needsChoice = true
		}
	}
	variants := []variant{{align: "aligned"}}
	if needsChoice {
		variants = []variant{{align: "drop-trailing"}, {align: "drop-leading"}}
	}
	var out []Candidate
	for _, v := range variants {
		c := Candidate{
			Key:        fmt.Sprintf("i2c/boundary=%s/align=%s", pref, v.align),
			Label:      fmt.Sprintf("I2C boundary=%s align=%s", pref, v.align),
			Confidence: 1.0,
			Status:     model.CandidatePending,
		}
		if pref == "after" {
			c.Reason = model.DiagBoundaryAmbiguous
			c.Confidence -= 0.1
		}
		if v.align != "aligned" {
			c.Reason = model.DiagMissingClockEdge
			c.Confidence -= 0.15
			c.Diagnostics = append(c.Diagnostics, model.Diagnostic{
				Code: model.DiagMissingClockEdge, Severity: "warn",
				Message: fmt.Sprintf("bit count is not a multiple of 9 (missing clock edge); candidate %q retained", v.align),
			})
		}
		for _, f := range frames {
			bits := f.bits
			if v.align == "drop-leading" {
				if r := len(bits) % 9; r != 0 {
					bits = bits[r:]
				}
			} else if v.align == "drop-trailing" {
				bits = bits[:len(bits)-len(bits)%9]
			}
			tx := Transaction{
				Bus:     "i2c",
				StartNS: f.startNS,
				EndNS:   f.endNS,
				Flags:   append([]string{}, f.flags...),
			}
			if f.noStart {
				tx.Flags = append(tx.Flags, "no_start")
			}
			for i := 0; i+9 <= len(bits); i += 9 {
				b := Byte{Bits: 8, Channel: "sda", StartNS: bits[i].timeNS, EndNS: bits[i+8].timeNS}
				for k := 0; k < 8; k++ {
					b.Value = b.Value<<1 | bits[i+k].value
					b.EdgeIDs = append(b.EdgeIDs, bits[i+k].edgeID)
				}
				if bits[i+8].value == 0 {
					b.Ack = "ack"
				} else {
					b.Ack = "nack"
				}
				b.EdgeIDs = append(b.EdgeIDs, bits[i+8].edgeID)
				tx.Bytes = append(tx.Bytes, b)
			}
			if rem := len(bits) % 9; rem != 0 && v.align == "aligned" {
				tx.Flags = append(tx.Flags, "partial")
			}
			if len(tx.Bytes) > 0 {
				c.Transactions = append(c.Transactions, tx)
			}
		}
		if c.Reason == "" {
			c.Label = "I2C unique decode"
			c.Key = "i2c/unique"
		}
		out = append(out, c)
	}
	return out
}

func hasEdgeAt(edges []model.Edge, t int64) bool {
	for _, e := range edges {
		if e.TimeOK && e.TimeNS == t {
			return true
		}
	}
	return false
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

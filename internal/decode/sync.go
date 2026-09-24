package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// DecodeSync decodes a custom synchronous clock+data bus. Frames are split
// on idle gaps (never stitched); a partial final byte forks alignment
// candidates exactly like the bus decoders.
func DecodeSync(m *model.Material, p model.Params) Result {
	res := Result{Kind: model.KindOK, Bus: "sync"}
	clk := m.ChannelByRole(model.RoleCLK)
	data := m.ChannelByRole(model.RoleData)
	if clk == nil || data == nil {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "capture has no clk/data pair for the custom sync bus",
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
	clks := clockEdges(m, *clk, p.ClockPolarity)
	if len(clks) == 0 {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error", Channel: clk.ID,
			Message: "no clock edges for the custom sync bus",
		})
		return res
	}
	tl := newTimeline(m, *data)
	type frame struct {
		bits  []i2cBit
		start int64
		end   int64
	}
	var frames []frame
	var cur *frame
	last := int64(-1)
	for _, c := range clks {
		t := c.edge.TimeNS
		if last >= 0 && t-last > p.GapNS && cur != nil && len(cur.bits) > 0 {
			frames = append(frames, *cur)
			cur = nil
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: model.DiagDataGap, Severity: "warn", Channel: clk.ID,
				EdgeID: c.edge.ID, TimeNS: t,
				Message: "sync clock idle beyond gap threshold: frame closed, not stitched",
			})
		}
		if cur == nil {
			cur = &frame{start: t}
		}
		cur.bits = append(cur.bits, i2cBit{value: tl.levelJustBefore(t), edgeID: c.edge.ID, timeNS: t})
		cur.end = t
		last = t
	}
	if cur != nil && len(cur.bits) > 0 {
		frames = append(frames, *cur)
	}
	if len(frames) == 0 {
		res.Kind = model.KindUnsolvable
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: model.DiagUnsolvable, Severity: "error",
			Message: "no bits sampled on the custom sync bus",
		})
		return res
	}
	misaligned := false
	for _, f := range frames {
		if len(f.bits)%8 != 0 {
			misaligned = true
		}
	}
	aligns := []string{"aligned"}
	if misaligned {
		aligns = []string{"drop-trailing", "drop-leading"}
	}
	var candidates []Candidate
	for _, align := range aligns {
		c := Candidate{
			Key:        fmt.Sprintf("sync/align=%s", align),
			Label:      fmt.Sprintf("SYNC align=%s", align),
			Confidence: 1.0,
			Status:     model.CandidatePending,
		}
		if align != "aligned" {
			c.Reason = model.DiagMissingClockEdge
			c.Confidence -= 0.15
			c.Diagnostics = append(c.Diagnostics, model.Diagnostic{
				Code: model.DiagMissingClockEdge, Severity: "warn",
				Message: fmt.Sprintf("bit count not a multiple of 8 (missing clock edge); candidate %q retained", align),
			})
		} else {
			c.Key = "sync/unique"
			c.Label = "SYNC unique decode"
		}
		for _, f := range frames {
			bits := f.bits
			if align == "drop-leading" {
				if r := len(bits) % 8; r != 0 {
					bits = bits[r:]
				}
			} else if align == "drop-trailing" {
				bits = bits[:len(bits)-len(bits)%8]
			}
			tx := Transaction{Bus: "sync", StartNS: f.start, EndNS: f.end}
			for i := 0; i+8 <= len(bits); i += 8 {
				b := Byte{Bits: 8, Channel: "data", StartNS: bits[i].timeNS, EndNS: bits[i+7].timeNS}
				for k := 0; k < 8; k++ {
					b.Value = b.Value<<1 | bits[i+k].value
					b.EdgeIDs = append(b.EdgeIDs, bits[i+k].edgeID)
				}
				tx.Bytes = append(tx.Bytes, b)
			}
			if len(tx.Bytes) > 0 {
				c.Transactions = append(c.Transactions, tx)
			}
		}
		candidates = append(candidates, c)
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

package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// decodeSync decodes a custom synchronous channel pair (clock + data).
// Sampling follows the configured clock polarity; a data gap closes the
// transaction without stitching; two data edges between clock edges fork a
// missing-clock hypothesis.
func decodeSync(channels []model.Channel, edges []model.Edge, levels []int, p Params) Result {
	clk := roleOf(channels, "clock")
	data := roleOf(channels, "data")
	if len(clk) == 0 || len(data) == 0 {
		return Result{Diagnostics: []Diag{{
			Position: "probe", Code: "missing_role",
			Message: "sync decode requires clock and data probe roles", Severity: "error",
		}}}
	}
	clkCh, dataCh := clk[0], data[0]
	sampleRising := p.ClockPolarity != "falling"

	type hypo struct {
		bits    []int
		bitEdge []int
		bytes   []Byte
		txns    []Transaction
		reasons map[string]bool
		conf    float64
		txStart int
		txEdge  int
		gap     bool
	}
	hypos := []*hypo{{reasons: map[string]bool{}, conf: 1.0}}
	var diags []Diag

	clkLevel := 0
	lastEdgeTime := int64(-1)
	dataMoved := false

	flush := func(h *hypo, atEdge int) {
		if len(h.bits) > 0 {
			diags = append(diags, Diag{
				Position: fmt.Sprintf("edge[%d]", atEdge),
				Code:     "partial_byte",
				Message:  fmt.Sprintf("sync transaction closed with %d unsampled bits", len(h.bits)),
				Severity: "warning",
			})
		}
		h.bits, h.bitEdge = nil, nil
		if len(h.bytes) > h.txStart {
			h.txns = append(h.txns, Transaction{
				Device:    "sync:" + dataCh,
				Kind:      "transfer",
				ByteStart: h.txStart,
				ByteEnd:   len(h.bytes),
				EdgeStart: h.txEdge,
				EdgeEnd:   atEdge,
				GapBefore: h.gap,
			})
		}
		h.txStart = len(h.bytes)
		h.txEdge = atEdge
		h.gap = false
	}

	for i, e := range edges {
		if lastEdgeTime >= 0 && e.TimeNS-lastEdgeTime > p.GapThresholdNS {
			for _, h := range hypos {
				if len(h.bits) > 0 || len(h.bytes) > h.txStart {
					h.reasons[ReasonDataGap] = true
					flush(h, i-1)
					h.gap = true
				}
			}
		}
		lastEdgeTime = e.TimeNS
		lv := levels[i]
		switch e.Channel {
		case clkCh:
			rising := lv == 1 && clkLevel == 0
			falling := lv == 0 && clkLevel == 1
			if (rising && sampleRising) || (falling && !sampleRising) {
				dv := levelAt(edges, levels, dataCh, i)
				for _, h := range hypos {
					h.bits = append(h.bits, dv)
					h.bitEdge = append(h.bitEdge, i)
					if len(h.bits) == 8 {
						val := 0
						for _, b := range h.bits {
							val = val<<1 | b
						}
						h.bytes = append(h.bytes, Byte{
							Value:     val,
							EdgeStart: h.bitEdge[0],
							EdgeEnd:   h.bitEdge[7],
							StartNS:   edges[h.bitEdge[0]].TimeNS,
							EndNS:     edges[h.bitEdge[7]].TimeNS,
						})
						h.bits, h.bitEdge = nil, nil
					}
				}
			}
			clkLevel = lv
			dataMoved = false
		case dataCh:
			if dataMoved {
				for _, h := range hypos {
					h.reasons[ReasonMissingClock] = true
					h.conf *= 0.5
				}
			}
			dataMoved = true
		}
	}
	for _, h := range hypos {
		flush(h, len(edges)-1)
	}

	var cands []Candidate
	for _, h := range hypos {
		if len(h.bytes) == 0 {
			continue
		}
		cands = append(cands, Candidate{
			Protocol:   "sync",
			Confidence: h.conf,
			Reasons:    sortedReasons(h.reasons),
			Detail:     fmt.Sprintf("sync sample-on-%s", p.ClockPolarity),
			StartEdge:  h.txns[0].EdgeStart,
			EndEdge:    h.txns[len(h.txns)-1].EdgeEnd,
			Bytes:      h.bytes,
			Txns:       h.txns,
		})
	}
	return Result{Diagnostics: diags, Candidates: cands}
}

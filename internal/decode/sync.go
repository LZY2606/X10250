package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// decodeSync decodes a custom synchronous clock+data stream. Frames start
// after an idle gap; each frame is FrameBits bits sampled on the configured
// clock edge. Clock polarity and boundary sampling are parameters, so a
// revision changes the derived layer only.
func decodeSync(cap *model.Capture, p *model.Params, r *model.Run) {
	clk := buildLine(cap, p.ClockChannel)
	data := buildLine(cap, p.DataChannel)
	if len(clk.edges) == 0 {
		r.Status = model.StatusSemanticUnsolv
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagSemanticUnsolv,
			Position: p.ClockChannel, Message: "no clock edges; cannot hypothesize"})
		return
	}
	frameBits := p.FrameBits
	if frameBits <= 0 {
		frameBits = 8
	}
	// sample edge relative to idle polarity
	sampleLevel := 1 - p.ClockPolarity
	if p.SampleEdge == "falling" {
		sampleLevel = p.ClockPolarity
	}

	var txs []model.Transaction
	var bits []bitSample
	txStart := int64(-1)
	lastClock := int64(-1)
	flush := func(end int64, truncated bool) {
		if len(bits) == 0 {
			return
		}
		var flags []string
		if truncated {
			flags = append(flags, "truncated-gap")
		}
		var bytes []model.Byte
		for i := 0; i+frameBits <= len(bits); i += frameBits {
			val := 0
			var refs []model.EdgeRef
			for k := 0; k < frameBits; k++ {
				val = val<<1 | bits[i+k].bit
				refs = append(refs, bits[i+k].clock)
			}
			bytes = append(bytes, NewByte(val, nil, refs, frameBits))
		}
		if len(bytes) > 0 {
			txs = append(txs, NewTransaction("sync-frame", "", 0, bytes, txStart, end, flags))
		}
		bits = nil
	}

	for _, e := range clk.edges {
		if p.GapSamples > 0 && lastClock >= 0 && e.Sample-lastClock > p.GapSamples {
			flush(lastClock, true)
			txStart = -1
		}
		if e.Level != sampleLevel {
			continue
		}
		if txStart < 0 {
			txStart = e.Sample
		}
		lastClock = e.Sample
		bits = append(bits, bitSample{data.levelAt(e.Sample, 0), model.EdgeRef{ChannelID: p.ClockChannel, Sample: e.Sample}})
	}
	flush(lastClock, false)

	conf := 1.0
	r.Candidates = append(r.Candidates, model.Candidate{
		Group: "sync:" + cap.ID, Hypothesis: fmt.Sprintf("sync-pol%d-%s", p.ClockPolarity, p.SampleEdge),
		Confidence: conf,
		Reasons: []string{fmt.Sprintf("clock polarity=%d sample=%s frame=%dbits",
			p.ClockPolarity, p.SampleEdge, frameBits)},
		Transactions: txs,
	})
}

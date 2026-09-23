package decode

import (
	"edgeworkbench/internal/model"
)

// decodeSync decodes the custom synchronous bus: EN frames a transaction,
// DATA is sampled on the configured clock polarity. The polarity is a
// versioned parameter, so revising it re-derives only this bus.
func decodeSync(c *model.Capture, p *model.Params, budget *candidateBudget) *busResult {
	clkCh, okCLK := chanByRole(c, "clk")
	dataCh, okDAT := chanByRole(c, "data")
	enCh, okEN := chanByRole(c, "en")
	if !okCLK || !okDAT || !okEN {
		return nil
	}
	res := &busResult{}
	col := &collector{seen: map[string]bool{}}

	wantLevel := 1
	if p.ClockPolarity == "falling" {
		wantLevel = 0
	}

	var txns []model.Transaction
	var cur *model.Transaction
	var bits []int
	var bitEdges []string
	var byteStarts []int64
	enLevel := 0
	for _, e := range c.Edges {
		switch e.Channel {
		case enCh.Alias:
			lvl := levelOf(enCh, e.Level)
			if lvl == 1 && enLevel == 0 {
				cur = &model.Transaction{Bus: "sync", Device: enCh.Alias, StartNs: e.TimeNs}
				bits, bitEdges, byteStarts = nil, nil, nil
			}
			if lvl == 0 && enLevel == 1 && cur != nil {
				cur.EndNs = e.TimeNs
				if len(cur.Bytes) > 0 {
					txns = append(txns, *cur)
				}
				cur = nil
			}
			enLevel = lvl
		case clkCh.Alias:
			if cur == nil || levelOf(clkCh, e.Level) != wantLevel {
				continue
			}
			bit := levelAt(c.Edges, dataCh, e.TimeNs, false)
			bits = append(bits, bit)
			bitEdges = append(bitEdges, e.ID)
			if len(bits)%8 == 1 {
				byteStarts = append(byteStarts, e.TimeNs)
			}
			if len(bits)%8 == 0 {
				val := 0
				for _, b := range bits[len(bits)-8:] {
					val = val<<1 | b
				}
				cur.Bytes = append(cur.Bytes, model.Byte{
					Index:   len(cur.Bytes),
					Value:   val,
					StartNs: byteStarts[len(byteStarts)-1],
					EndNs:   e.TimeNs,
					EdgeIDs: append([]string(nil), bitEdges[len(bitEdges)-8:]...),
				})
			}
		}
	}
	if cur != nil {
		cur.EndNs = c.Edges[len(c.Edges)-1].TimeNs
		if len(cur.Bytes) > 0 {
			txns = append(txns, *cur)
		}
	}
	if len(txns) == 0 {
		return nil
	}
	res.candidates = append(res.candidates, model.Candidate{
		Bus: "sync", Reason: "primary", Confidence: 1.0, Transactions: txns,
	})
	res.events = col.events
	return res
}

package decoder

import "edgebench/internal/domain"

// decodeCustom is the generic synchronous decoder used by the custom sync
// protocol. It samples the data line on the active clock edge selected by
// clock polarity (leading edge when CPHA=0). Missing clock edges and data
// gaps are treated with the same structured rules as SPI.
func decodeCustom(channels map[string]*domain.Channel, edges []domain.Edge, p domain.DecoderParams) []domain.Transaction {
	if p.SCLK == "" || p.MOSI == "" {
		return nil
	}
	sp := p
	sp.Protocol = "custom"
	if sp.BitOrder == "" {
		sp.BitOrder = "msb"
	}
	if sp.CS == "" {
		// No chip select: keep CPHA semantics but remove frame reliance on CS.
		txns := decodeSPILess(channels, edges, sp)
		for i := range txns {
			txns[i].Protocol = "custom"
		}
		return txns
	}
	txns := decodeSPI(channels, edges, sp)
	for i := range txns {
		txns[i].Protocol = "custom"
	}
	return txns
}

// decodeSPILess samples a free-running clock into bytes split by gaps.
func decodeSPILess(channels map[string]*domain.Channel, edges []domain.Edge, p domain.DecoderParams) []domain.Transaction {
	sclk := signal(findChan(channels, p.SCLK), edges)
	data := signal(findChan(channels, p.MOSI), edges)
	if len(sclk) == 0 {
		return nil
	}
	gap := p.GapLimitPS
	if gap <= 0 {
		gap = estimateClockPeriod(sclk) * 16
	}
	segs := splitOnGap(sclk, gap)
	var txns []domain.Transaction
	for _, seg := range segs {
		txn := domain.Transaction{
			BusID: p.BusID, Protocol: "custom",
			StartPS: seg[0].ps, EndPS: seg[len(seg)-1].ps, Status: "tentative",
			Candidates: []domain.Candidate{{
				BusID: p.BusID, Mode: "custom-sync", Adopted: true,
				Confidence: 0.9, RulesVersion: RulesVersion,
			}},
		}
		bs := decodeSPISegment(seg, data, nil, p.CPOL, p.CPHA, p.BitOrder)
		txn.Candidates[0].Bytes = bs
		if len(seg)%2 != 0 {
			txn.Evidences = append(txn.Evidences, domain.Evidence{
				Kind: domain.EvMissingClock, StartPS: txn.StartPS, EndPS: txn.EndPS,
				EdgeIDs: edgeIDs(seg), Message: "odd clock edges; final word incomplete",
				Policy: missingPolicy,
			})
			txn.Candidates[0].Confidence = 0.5
		}
		txns = append(txns, txn)
	}
	return assignStableIDs(txns, p)
}

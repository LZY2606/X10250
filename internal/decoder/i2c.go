package decoder

import (
	"sort"

	"edgebench/internal/domain"
	"edgebench/internal/ident"
)

// i2cBit is one SCL sample opportunity inside an I2C frame.
type i2cBit struct {
	value   int
	clockID string
	dataID  string
	ps      int64
}

// decodeI2C reconstructs I2C transactions from logical SCL/SDA signals.
//
// Structural rules (each also emitted as machine-readable Evidence):
//   - repeated START keeps the frame inside one transaction (continue)
//   - clock stretching: an abnormally long SCL low holds the in-progress bit
//   - missing clock edge: a byte window without all 9 clocks stays partial;
//     multiple legal interpretations are retained as candidates and never
//     padded silently
//   - data gap: silence beyond GapLimitPS splits into a new transaction;
//     bytes are never stitched across the gap
func decodeI2C(channels map[string]*domain.Channel, edges []domain.Edge, p domain.DecoderParams) []domain.Transaction {
	scl := signal(findChan(channels, p.SCL), edges)
	sda := signal(findChan(channels, p.SDA), edges)
	if len(scl) == 0 && len(sda) == 0 {
		return nil
	}

	type marker struct {
		ps   int64
		kind string
		edge string
	}
	markers := []marker{}
	for _, e := range sda {
		if hi, _ := levelAt(scl, e.ps, 1); hi == 1 {
			k := "start"
			if e.level == 1 {
				k = "stop"
			}
			markers = append(markers, marker{e.ps, k, e.id})
		}
	}
	if len(markers) == 0 {
		return nil
	}
	sort.Slice(markers, func(i, j int) bool { return markers[i].ps < markers[j].ps })

	rises := risingEdges(scl)
	medianLow := medianLowTime(scl)
	stretch := p.StretchHoldPS
	if stretch <= 0 && medianLow > 0 {
		stretch = int64(float64(medianLow) * 2.5)
	}
	gap := p.GapLimitPS
	if gap <= 0 && medianLow > 0 {
		gap = medianLow * 24
	}

	sampleAt := func(r ev) (int, string) {
		lvl, id := 1, ""
		if p.SampleBoundary == "post" {
			lvl, id = levelAt(sda, r.ps+1, 1)
			return lvl, id
		}
		for _, d := range sda {
			if d.ps < r.ps {
				lvl, id = d.level, d.id
			} else {
				break
			}
		}
		return lvl, id
	}

	var txns []domain.Transaction
	var cur *domain.Transaction
	var bits []i2cBit
	var txEv []domain.Evidence
	var lastClockPS int64

	// ri indexes clock rises already consumed.
	ri := 0
	collect := func(upto int64, closing bool) {
		for ri < len(rises) && (rises[ri].ps < upto || closing && rises[ri].ps <= upto) {
			r := rises[ri]
			if stretch > 0 && len(bits) > 0 && r.ps-lastClockPS > stretch {
				e := domain.Evidence{
					Kind: domain.EvClockStretch, StartPS: lastClockPS, EndPS: r.ps,
					EdgeIDs: []string{bits[len(bits)-1].clockID, r.id},
					Message: "SCL held low beyond stretch budget; bit waits for release",
					Policy:  stretchPolicy,
					Detail:  map[string]any{"lowPs": r.ps - lastClockPS, "budgetPs": stretch},
				}
				txEv = append(txEv, e)
			}
			if gap > 0 && len(bits) > 0 && r.ps-lastClockPS > gap {
				e := domain.Evidence{
					Kind: domain.EvDataGap, StartPS: lastClockPS, EndPS: r.ps,
					EdgeIDs: []string{r.id},
					Message: "silence exceeds gap limit; stream is split, bytes are not stitched",
					Policy:  gapPolicySplit,
					Detail:  map[string]any{"gapPs": r.ps - lastClockPS, "limitPs": gap},
				}
				txEv = append(txEv, e)
				bits = bits[:0]
			}
			v, did := sampleAt(r)
			bits = append(bits, i2cBit{value: v, clockID: r.id, dataID: did, ps: r.ps})
			lastClockPS = r.ps
			ri++
			if len(bits) == 9 {
				emitI2CByte(cur, bits)
				bits = bits[:0]
			}
		}
	}

	openTx := func(m marker) {
		txEv = nil
		bits = bits[:0]
		lastClockPS = m.ps
		txns = append(txns, domain.Transaction{
			BusID: p.BusID, Protocol: "i2c", StartPS: m.ps, Status: "tentative",
			Candidates: []domain.Candidate{{
				BusID: p.BusID, Mode: "i2c-standard", Adopted: true,
				Confidence: 1, RulesVersion: RulesVersion,
			}},
		})
		cur = &txns[len(txns)-1]
	}

	for mi, m := range markers {
		switch m.kind {
		case "start":
			if cur == nil {
				openTx(m)
			} else {
				collect(m.ps, false)
				if len(bits) > 0 {
					partialI2C(cur, bits, txEv, p, m.ps, "repeated START arrived mid-byte")
					bits = bits[:0]
				}
				e := domain.Evidence{
					Kind: domain.EvRepeatedStart, StartPS: m.ps, EndPS: m.ps,
					EdgeIDs: []string{m.edge},
					Message: "repeated START; frame continues on the same transaction",
					Policy:  repeatPolicy,
				}
				txEv = append(txEv, e)
				lastClockPS = m.ps
			}
			upto := int64(1 << 62)
			closing := true
			if mi+1 < len(markers) {
				upto = markers[mi+1].ps
				closing = markers[mi+1].kind == "stop"
			}
			collect(upto, closing)
		case "stop":
			if cur == nil {
				continue
			}
			collect(m.ps, true)
			if len(bits) > 0 {
				partialI2C(cur, bits, txEv, p, m.ps, "STOP arrived mid-byte")
				bits = bits[:0]
			}
			cur.EndPS = m.ps
			closeI2C(cur, txEv)
			cur = nil
		}
	}
	if cur != nil {
		if len(bits) > 0 {
			partialI2C(cur, bits, txEv, p, lastClockPS, "capture ended without STOP")
		}
		cur.EndPS = lastClockPS
		closeI2C(cur, txEv)
	}
	return assignStableIDs(txns, p)
}

func risingEdges(s []ev) []ev {
	out := []ev{}
	prev := 1
	for _, e := range s {
		if e.level == 1 && prev == 0 {
			out = append(out, e)
		}
		prev = e.level
	}
	return out
}

func medianLowTime(scl []ev) int64 {
	var lows []int64
	var fallPS int64
	wasLow := false
	for _, e := range scl {
		if e.level == 0 {
			fallPS, wasLow = e.ps, true
		} else if wasLow {
			lows = append(lows, e.ps-fallPS)
			wasLow = false
		}
	}
	if len(lows) == 0 {
		return 0
	}
	sort.Slice(lows, func(i, j int) bool { return lows[i] < lows[j] })
	return lows[len(lows)/2]
}

func emitI2CByte(cur *domain.Transaction, bits []i2cBit) {
	c := &cur.Candidates[0]
	v := 0
	ids := make([]string, 0, 18)
	for k := 0; k < 8; k++ {
		v |= bits[k].value << (7 - k)
		ids = append(ids, bits[k].clockID, bits[k].dataID)
	}
	ids = append(ids, bits[8].clockID, bits[8].dataID)
	b := domain.DecodedByte{
		Index:   len(c.Bytes),
		Value:   v,
		Ack:     ackName(bits[8].value),
		StartPS: bits[0].ps,
		EndPS:   bits[8].ps,
		EdgeIDs: uniq(ids),
	}
	if b.Index == 0 {
		b.Role = "address"
		cur.Device = "0x" + hex2(v>>1)
	} else {
		b.Role = "data"
	}
	if b.Ack == "nack" {
		txnNack(cur, b)
	}
	c.Bytes = append(c.Bytes, b)
}

func txnNack(cur *domain.Transaction, b domain.DecodedByte) {
	e := domain.Evidence{
		Kind: domain.EvNACK, StartPS: b.StartPS, EndPS: b.EndPS,
		EdgeIDs: b.EdgeIDs, Message: "slave returned NACK", Policy: "flag_only",
	}
	cur.Evidences = append(cur.Evidences, e)
	cur.Candidates[0].Evidences = append(cur.Candidates[0].Evidences, e)
}

func partialI2C(cur *domain.Transaction, bits []i2cBit, txEv []domain.Evidence, p domain.DecoderParams, endPS int64, why string) {
	ids := make([]string, 0, len(bits)*2)
	for _, b := range bits {
		ids = append(ids, b.clockID, b.dataID)
	}
	e := domain.Evidence{
		Kind: domain.EvMissingClock, StartPS: bits[0].ps, EndPS: endPS,
		EdgeIDs: uniq(ids), Message: why + "; legal interpretations retained as candidates",
		Policy: missingPolicy,
		Detail: map[string]any{"bitsReceived": len(bits), "bitsRequired": 9},
	}
	txEv = append(txEv, e)
	cur.Evidences = append(cur.Evidences, e)
	c := &cur.Candidates[0]
	c.Evidences = append(c.Evidences, e)
	c.Confidence = 0.5
	alt := domain.Candidate{
		BusID: p.BusID, Mode: "i2c-incomplete", Confidence: 0.25,
		RulesVersion: RulesVersion, Reason: "byte cannot be completed with available clocks",
		Evidences: []domain.Evidence{e},
		Detail:    map[string]any{"partialBits": len(bits)},
	}
	cur.Candidates = append(cur.Candidates, alt)
}

func closeI2C(cur *domain.Transaction, txEv []domain.Evidence) {
	cur.Evidences = mergeEvidence(cur.Evidences, txEv)
	c := &cur.Candidates[0]
	c.Evidences = mergeEvidence(c.Evidences, cur.Evidences)
	conf := 1.0
	for _, b := range c.Bytes {
		if b.Ack == "nack" {
			conf -= 0.1
		}
	}
	c.Confidence = clamp(conf)
	c.RulesVersion = RulesVersion
	cur.RulesVersion = RulesVersion
	cur.Key = ident.Parts("txn", cur.BusID, cur.Protocol, cur.StartPS)
}

func mergeEvidence(a, b []domain.Evidence) []domain.Evidence {
	seen := map[string]bool{}
	for _, e := range a {
		seen[string(e.Kind)+"/"+joinIDs(e.EdgeIDs)] = true
	}
	for _, e := range b {
		k := string(e.Kind) + "/" + joinIDs(e.EdgeIDs)
		if !seen[k] {
			a = append(a, e)
			seen[k] = true
		}
	}
	return a
}

func joinIDs(ids []string) string {
	out := ""
	for _, s := range ids {
		out += s + ","
	}
	return out
}

func ackName(v int) string {
	if v == 0 {
		return "ack"
	}
	return "nack"
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func hex2(v int) string {
	const h = "0123456789abcdef"
	return string([]byte{h[(v>>4)&0xf], h[v&0xf]})
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

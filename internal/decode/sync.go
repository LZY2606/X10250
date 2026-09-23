package decode

import (
	"fmt"
)

// decodeSync decodes a custom synchronous bus: one clock, one data,
// 8-bit MSB-first frames separated by gaps (never stitched).
func decodeSync(m *Material, edges []NormEdge, p *Params) ([]Candidate, []Diagnostic, []Event, bool) {
	clkAlias := findChan(m, "sync", "clock")
	dataAlias := findChan(m, "sync", "data")
	var diags []Diagnostic
	if clkAlias == "" || dataAlias == "" {
		diags = append(diags, Diagnostic{Severity: "error", Code: "sync_channels_missing",
			Message: "sync bus requires clock and data channels"})
		return nil, diags, nil, false
	}
	ch := byChannel(edges)
	clk, data := ch[clkAlias], ch[dataAlias]
	if len(clk) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "sync_no_clock",
			Message: "no clock edges on " + clkAlias, Channel: clkAlias})
		return nil, diags, nil, false
	}
	cfg := m.channelConfigs()[clkAlias]
	pol := p.polarityFor("sync", cfg)
	sampleLevel := 1
	if pol == "falling" {
		sampleLevel = 0
	}
	dataLevel := levelAt(data)

	var samps []NormEdge
	for _, e := range clk {
		if e.Level == sampleLevel {
			samps = append(samps, e)
		}
	}
	if len(samps) == 0 {
		diags = append(diags, Diagnostic{Severity: "error", Code: "sync_no_sampling_edge",
			Message: "no " + pol + " clock edges to sample on", Channel: clkAlias})
		return nil, diags, nil, false
	}

	// Frames are separated by data gaps; bytes are never stitched across.
	var frames [][]NormEdge
	var events []Event
	for _, e := range samps {
		if n := len(frames); n > 0 {
			last := frames[n-1][len(frames[n-1])-1]
			if e.TimePS-last.TimePS > p.GapThresholdPS {
				events = append(events, Event{Kind: EvGapSplit, Detail: map[string]any{
					"bus": "sync", "gap_start_ps": last.TimePS, "gap_end_ps": e.TimePS,
					"reason": "data_gap_not_stitched"}})
				frames = append(frames, nil)
			}
		}
		if len(frames) == 0 {
			frames = append(frames, nil)
		}
		frames[len(frames)-1] = append(frames[len(frames)-1], e)
	}

	decode := func(flipAt int64) ([]Transaction, []int64) {
		var txs []Transaction
		var boundary []int64
		for _, fr := range frames {
			var bits []i2cBit
			for _, s := range fr {
				if flipAt < 0 {
					for _, e := range data {
						d := e.TimePS - s.TimePS
						if d < 0 {
							d = -d
						}
						if d <= p.BoundaryWindowPS {
							boundary = append(boundary, s.TimePS)
							break
						}
					}
				}
				lvl := dataLevel(s.TimePS)
				if lvl < 0 {
					lvl = 0
				}
				if s.TimePS == flipAt {
					lvl = 1 - lvl
				}
				bits = append(bits, i2cBit{t: s.TimePS, val: lvl, edgeIdx: s.Index})
			}
			tx := Transaction{Decoder: "sync", StartPS: fr[0].TimePS, EndPS: fr[len(fr)-1].TimePS}
			full := len(bits) / 8
			for i := 0; i < full; i++ {
				grp := bits[i*8 : (i+1)*8]
				val := 0
				for _, b := range grp {
					val = (val << 1) | b.val
				}
				tx.Bytes = append(tx.Bytes, Byte{
					Value: val, EdgeStart: grp[0].edgeIdx, EdgeEnd: grp[7].edgeIdx,
					TimeStartPS: grp[0].t, TimeEndPS: grp[7].t, Confidence: 1,
				})
			}
			if rem := len(bits) % 8; rem != 0 {
				t := bits[len(bits)-1].t
				diags = append(diags, Diagnostic{Severity: "warning", Code: "partial_byte",
					Message: fmt.Sprintf("%d dangling bits dropped at frame end", rem), TimePS: &t})
			}
			if len(tx.Bytes) > 0 {
				tx.ID = shortID("tx", fmt.Sprintf("sync|%s|%d|%d", pol, tx.StartPS, tx.EndPS))
				txs = append(txs, tx)
			}
		}
		return txs, boundary
	}

	txs, boundary := decode(-1)
	cands := []Candidate{{
		ID: shortID("cand", "sync|"+pol), Bus: "sync", Confidence: 0.95,
		Reasons: []string{}, Params: map[string]string{"clock_polarity": pol},
		Transactions: txs,
	}}
	for _, hit := range boundary {
		altTxs, _ := decode(hit)
		cands = append(cands, Candidate{
			ID: shortID("cand", fmt.Sprintf("sync|%s|flip%d", pol, hit)), Bus: "sync",
			Confidence: 0.45, Reasons: []string{ReasonBoundarySample},
			Params:       map[string]string{"clock_polarity": pol, "flip_bit_at_ps": fmt.Sprint(hit)},
			Transactions: altTxs,
		})
		events = append(events, Event{Kind: EvCandidateRetained, Code: ReasonBoundarySample,
			Detail: map[string]any{"bus": "sync", "time_ps": hit, "alternatives": 2}})
	}
	return cands, diags, events, countBytes(txs) > 0
}

package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// runSync decodes the custom synchronous channel: DATA sampled on the
// configured clock polarity. Boundary-window edges fork candidates; gaps
// terminate transactions without stitching.
func runSync(cap *model.Capture, p model.Params, ov map[string]bool) ([]model.Transaction, []ambiguity, []model.Diagnostic) {
	clkCh := findChannel(cap, "SYNC_CLK")
	dataCh := findChannel(cap, "SYNC_DATA")
	if clkCh == nil || dataCh == nil {
		return nil, nil, nil
	}
	clk, data := buildLine(cap, "SYNC_CLK"), buildLine(cap, "SYNC_DATA")

	wantLevel := 1 // rising
	if p.ClockPolarity == "falling" {
		wantLevel = 0
	}
	var txs []model.Transaction
	var ambs []ambiguity
	var diag []model.Diagnostic
	var tx *model.Transaction
	var bits []int
	var refs []int
	var bitStart int64
	lastT := int64(-1)
	byteCount := 0

	flush := func(flag string, t int64) {
		if tx == nil {
			return
		}
		if len(bits) > 0 {
			diag = append(diag, model.Diagnostic{
				Code: "SYNC_PARTIAL_BYTE", Severity: "warning", TimeNs: t, EdgeIndex: -1,
				Message: fmt.Sprintf("transaction closed with %d dangling bits", len(bits)),
			})
			bits, refs = nil, nil
		}
		if flag != "" {
			tx.Flags = append(tx.Flags, flag)
		}
		tx.EndNs = t
		txs = append(txs, *tx)
		tx = nil
	}

	for _, e := range clk.edges {
		if e.level != wantLevel {
			continue
		}
		if tx != nil && lastT >= 0 && e.t-lastT > p.GapThresholdNs {
			diag = append(diag, model.Diagnostic{
				Code: "SYNC_DATA_GAP", Severity: "warning", TimeNs: lastT, EdgeIndex: -1,
				Message: fmt.Sprintf("data gap of %d ns; transaction terminated, not stitched", e.t-lastT),
			})
			flush("gap_terminated", lastT)
		}
		lastT = e.t
		if tx == nil {
			tx = &model.Transaction{
				ID: fmt.Sprintf("sync-tx-%d", e.t),
				Protocol: "sync", Device: "SYNC",
				StartNs: e.t, EndNs: e.t,
			}
		}
		val := data.levelAt(e.t)
		if val < 0 {
			val = 1
		}
		// boundary sampling: data edge inside the window before the clock edge
		for _, de := range data.edges {
			if de.t < e.t && e.t-de.t <= p.BoundaryWindowNs {
				ambs = append(ambs, ambiguity{
					reason: "boundary_sampling", timeNs: e.t, channel: "SYNC_DATA",
					primary:   "sample_after_edge",
					alternate: "sample_before_edge",
					diagnostic: model.Diagnostic{
						Code: "SYNC_BOUNDARY_SAMPLE", Severity: "warning", TimeNs: e.t, EdgeIndex: e.idx,
						Message: fmt.Sprintf("DATA edge %d ns before clock edge is inside the boundary window", e.t-de.t),
					},
				})
				if ov[ambKey("boundary_sampling", e.t)] {
					val = 1 - de.level
				}
			}
		}
		if len(bits) == 0 {
			bitStart = e.t
		}
		bits = append(bits, val)
		refs = append(refs, e.idx)
		if len(bits) == 8 {
			v := 0
			for _, b := range bits {
				v = v<<1 | b
			}
			tx.Bytes = append(tx.Bytes, model.Byte{
				ID: fmt.Sprintf("sync-b-%d-%02x-%d", bitStart, v, byteCount),
				Value: v, Bits: 8, Ack: "none",
				StartNs: bitStart, EndNs: e.t, EdgeRefs: append([]int(nil), refs...),
			})
			byteCount++
			tx.EndNs = e.t
			bits, refs = nil, nil
		}
	}
	if tx != nil {
		flush("", lastT)
	}
	return txs, ambs, diag
}

func decodeSync(cap *model.Capture, p model.Params) model.DecodeResult {
	base, ambs, diags := runSync(cap, p, nil)
	res := model.DecodeResult{Diagnostics: diags}
	if len(base) > 0 {
		res.Candidates = append(res.Candidates, makeCandidate("sync", "base", confidence(len(ambs), true), base, diags))
	}
	for _, a := range ambs {
		txs, _, _ := runSync(cap, p, map[string]bool{ambKey(a.reason, a.timeNs): true})
		if len(txs) == 0 {
			continue
		}
		hyp := fmt.Sprintf("%s=%s", a.reason, a.alternate)
		res.Candidates = append(res.Candidates, makeCandidate("sync", hyp, confidence(len(ambs), false), txs, append(append([]model.Diagnostic(nil), diags...), a.diagnostic)))
	}
	return res
}

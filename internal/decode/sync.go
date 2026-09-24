package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// decodeSync 自定义同步协议：时钟极性不明时保留多个候选；数据缺口切段不拼接。
func decodeSync(bus *model.BusSpec, sigs map[string]*signal, res *model.Result) {
	clk := needChan(res, bus, sigs, bus.Clock, "SYNC_CLK")
	data := needChan(res, bus, sigs, bus.Data, "SYNC_DATA")
	if clk == nil || data == nil {
		return
	}
	wordBits := bus.BitsPerWord
	if wordBits <= 0 {
		wordBits = 8
	}
	polarities := []string{"rising"}
	unknown := false
	switch bus.ClockPolarity {
	case "rising", "falling":
		polarities = []string{bus.ClockPolarity}
	default:
		polarities = []string{"rising", "falling"}
		unknown = true
	}

	// 按时钟边沿缺口切段：缺口不拼接成交易
	var groups [][]model.Edge
	for _, e := range clk.edges {
		if n := len(groups); n > 0 {
			last := groups[n-1][len(groups[n-1])-1]
			if bus.GapThresholdNs > 0 && e.T-last.T > bus.GapThresholdNs {
				groups = append(groups, nil)
			}
		}
		if len(groups) == 0 {
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], e)
	}

	budget := budgetOf(bus)
	for _, grp := range groups {
		if len(grp) == 0 {
			continue
		}
		startNs := grp[0].T
		endNs := grp[len(grp)-1].T
		txnID := model.ShortID("txn", bus.Name, "sync", fmt.Sprint(startNs))
		var diags []model.Diagnostic
		if unknown {
			diags = append(diags, model.Diagnostic{
				Code: "unknown_mode", Bus: bus.Name, TimeNs: startNs,
				Message: "时钟极性不明：保留 rising/falling 两个合法候选"})
		}
		var cands []model.Candidate
		exhausted := false
		for _, pol := range polarities {
			want := 1
			if pol == "falling" {
				want = 0
			}
			var bits []bitV
			anomalies := 0
			for _, e := range grp {
				if e.Level != want {
					continue
				}
				bv, bnd := sampleAt(data, e.T, bus.BoundaryEpsilonNs)
				if bnd {
					anomalies++
					diags = append(diags, model.Diagnostic{
						Code: "boundary", Bus: bus.Name, TimeNs: e.T,
						Message: fmt.Sprintf("边界采样：数据沿落在采样沿 ±%d ns 窗口内（%s）",
							bus.BoundaryEpsilonNs, pol)})
				}
				bits = append(bits, bv)
			}
			nwords := len(bits) / wordBits
			if rem := len(bits) % wordBits; rem > 0 {
				diags = append(diags, model.Diagnostic{
					Code: "truncated", Bus: bus.Name, TimeNs: endNs,
					Message: fmt.Sprintf("截断：%d 个残缺位被丢弃（%s）", rem, pol)})
				anomalies++
			}
			remaining := budget - len(cands)
			if remaining <= 0 {
				exhausted = true
				break
			}
			combos, ex := forkCandidates(bits[:nwords*wordBits], wordBits, remaining, nil)
			if ex {
				exhausted = true
			}
			for _, combo := range combos {
				var bytes []model.Byte
				var hexParts string
				for w := 0; w < nwords; w++ {
					wb := combo[w*wordBits : (w+1)*wordBits]
					v := bitsValue(wb)
					hexParts += fmt.Sprintf("%02x;", v)
					amb := false
					var refs []model.EdgeRef
					for _, b := range wb {
						if b.ambig {
							amb = true
						}
						if b.ref.Channel != "" {
							refs = append(refs, b.ref)
						}
					}
					bytes = append(bytes, model.Byte{
						Value: v, Bits: wordBits, Role: "word",
						StartNs: wb[0].t, EndNs: wb[wordBits-1].t,
						Ambig: amb, EdgeRefs: refs,
					})
				}
				candID := model.ShortID("cand", txnID, pol, hexParts)
				for i := range bytes {
					bytes[i].ID = fmt.Sprintf("%s/b%d", candID, i)
				}
				reason := "时钟极性 " + pol
				if unknown {
					reason = "时钟极性不明，候选 " + pol
				}
				cands = append(cands, model.Candidate{
					ID: candID, Decoder: "sync-" + pol,
					Params:     map[string]string{"polarity": pol},
					Bytes:      bytes,
					Confidence: confOf(anomalies),
					Anomalies:  anomalies,
					Reason:     reason,
				})
			}
		}
		if exhausted {
			diags = append(diags, model.Diagnostic{
				Code: "budget_exhausted", Bus: bus.Name, TimeNs: endNs,
				Message: fmt.Sprintf("资源预算耗尽：候选数超过预算 %d", budget)})
		}
		if len(cands) > budget {
			cands = cands[:budget]
		}
		sortCandidates(cands)
		res.Transactions = append(res.Transactions, model.Transaction{
			ID: txnID, Bus: bus.Name, Protocol: "sync",
			StartNs: startNs, EndNs: endNs, Status: "ok",
			Candidates: cands, Diagnostics: diags,
		})
	}
}

package core

import (
	"fmt"
	"sort"
	"strings"
)

// DecodeSync 译码自定义同步协议（CLK+DATA）。
// 时钟极性不明（unknown）时保留 rising/falling 两个候选；
// 时钟缺边导致的部分字保留残缺并附诊断。
func DecodeSync(clk, data []Edge, p SyncParams, budget *Budget) ([]Transaction, []Diagnostic) {
	sort.SliceStable(clk, func(i, j int) bool { return clk[i].TPS < clk[j].TPS })
	var dataTimes []int64
	for _, e := range data {
		dataTimes = append(dataTimes, e.TPS)
	}
	if len(clk) == 0 {
		if len(data) > 0 {
			dg := Diagnostic{Layer: "decode", Pos: "sync", Severity: "error", Code: "no_clock",
				Message: "存在 DATA 边沿但没有任何 CLK 边沿：语义无解，无法采样"}
			return nil, []Diagnostic{dg}
		}
		return nil, nil
	}
	// 按周期间隙把时钟边沿分成若干突发（burst），每个突发一笔交易。
	minGap := int64(-1)
	for i := 1; i < len(clk); i++ {
		g := clk[i].TPS - clk[i-1].TPS
		if g > 0 && (minGap < 0 || g < minGap) {
			minGap = g
		}
	}
	if minGap <= 0 {
		minGap = 1
	}
	breakGap := minGap * 3
	type burst struct{ edges []Edge }
	var bursts []burst
	for _, e := range clk {
		if len(bursts) == 0 || e.TPS-bursts[len(bursts)-1].edges[len(bursts[len(bursts)-1].edges)-1].TPS > breakGap {
			bursts = append(bursts, burst{})
		}
		bursts[len(bursts)-1].edges = append(bursts[len(bursts)-1].edges, e)
	}
	type syncMode struct {
		rising bool
		tag    string
		w      float64
	}
	var modes []syncMode
	switch p.SampleEdge {
	case "rising":
		modes = append(modes, syncMode{true, "rising", 1})
	case "falling":
		modes = append(modes, syncMode{false, "falling", 1})
	default: // unknown → 保留两个候选
		modes = append(modes, syncMode{true, "edge=rising", 0.5}, syncMode{false, "edge=falling", 0.5})
	}
	var txs []Transaction
	var diags []Diagnostic
	for _, b := range bursts {
		var ids []string
		for _, e := range b.edges {
			ids = append(ids, e.ID)
		}
		id := ShortID("TX-", "sync", strings.Join(ids, ","), p.SampleEdge, p.BitsPerWord)
		var all []Variant
		for _, m := range modes {
			var bits []Bit
			for _, e := range b.edges {
				if (e.Level == 1) != m.rising {
					continue
				}
				bits = append(bits, Bit{Value: levelAt(data, e.TPS, 0), ClockEdgeID: e.ID,
					Ambiguous: nearBoundary(dataTimes, e.TPS, 5_000)})
			}
			opts, d := spiWordOpts(id, "", bits, p.BitsPerWord)
			diags = append(diags, d...)
			vs, blown := buildVariants(id, opts, budget)
			if blown {
				diags = append(diags, Diagnostic{Layer: "decode", Pos: id, Severity: "error", Code: "budget_exhausted",
					Message: "候选数量超过资源预算"})
			}
			for i := range vs {
				if m.tag != "rising" && m.tag != "falling" || len(modes) > 1 {
					lbl := m.tag
					if vs[i].Label != "base" {
						lbl += "+" + vs[i].Label
					}
					vs[i].Label = lbl
					vs[i].ID = id + ":V:" + lbl
					for j := range vs[i].Bytes {
						vs[i].Bytes[j].ID = fmt.Sprintf("%s:B%d", vs[i].ID, vs[i].Bytes[j].Index)
					}
				}
				vs[i].Confidence *= m.w
			}
			all = append(all, vs...)
		}
		var sum float64
		for _, v := range all {
			sum += v.Confidence
		}
		if sum == 0 {
			sum = 1
		}
		for i := range all {
			all[i].Confidence /= sum
		}
		sort.SliceStable(all, func(i, j int) bool {
			if all[i].Confidence != all[j].Confidence {
				return all[i].Confidence > all[j].Confidence
			}
			return all[i].Label < all[j].Label
		})
		status := StatusOK
		if len(all) > 1 {
			status = StatusAmbiguous
		}
		txs = append(txs, Transaction{
			ID: id, Protocol: "sync", StartPS: b.edges[0].TPS, EndPS: b.edges[len(b.edges)-1].TPS,
			Status: status, Variants: all, EdgeIDs: ids,
		})
	}
	return txs, diags
}

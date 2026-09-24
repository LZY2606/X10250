package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// decodeSPI 按片选区间译码；SPI mode 不明时保留多个候选，边界采样展开歧义位。
func decodeSPI(bus *model.BusSpec, sigs map[string]*signal, res *model.Result) {
	clk := needChan(res, bus, sigs, bus.Clock, "SCLK")
	mosi := needChan(res, bus, sigs, bus.Data, "MOSI")
	miso := needChan(res, bus, sigs, bus.Data2, "MISO")
	if clk == nil || mosi == nil || miso == nil {
		return
	}
	if len(bus.ChipSelects) == 0 {
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: "unsolvable", Bus: bus.Name,
			Message: "spi bus 未配置片选：无法归属交易，未用默认值顶替"})
		return
	}
	for _, csAlias := range bus.ChipSelects {
		cs := needChan(res, bus, sigs, csAlias, "CS")
		if cs == nil {
			continue
		}
		active := 0
		if bus.CSActive == "high" {
			active = 1
		}
		for _, iv := range activeIntervals(cs, active) {
			decodeSPIInterval(bus, clk, mosi, miso, csAlias, iv, res)
		}
	}
}

func activeIntervals(cs *signal, active int) [][2]int64 {
	var ivs [][2]int64
	lvl := cs.initial()
	start := int64(-1)
	last := int64(0)
	for _, e := range cs.edges {
		if e.Level == active && lvl != active {
			start = e.T
		}
		if e.Level != active && lvl == active && start >= 0 {
			ivs = append(ivs, [2]int64{start, e.T})
			start = -1
		}
		lvl = e.Level
		last = e.T
	}
	if start >= 0 {
		ivs = append(ivs, [2]int64{start, last})
	}
	return ivs
}

// resolveModes：mode 不明时用时钟空闲电平收窄候选；无法收窄则保留全部。
func resolveModes(spec string, clkIdle int) []int {
	switch spec {
	case "0", "1", "2", "3":
		return []int{int(spec[0] - '0')}
	}
	switch clkIdle {
	case 0:
		return []int{0, 1}
	case 1:
		return []int{2, 3}
	}
	return []int{0, 1, 2, 3}
}

func sampleAt(sig *signal, t, eps int64) (bitV, bool) {
	v := sig.levelAt(t)
	idx := sig.lastEdgeAtOrBefore(t)
	var ref model.EdgeRef
	if idx >= 0 {
		ref = model.EdgeRef{Channel: sig.alias, Index: idx, T: sig.edges[idx].T}
	}
	boundary := false
	if eps > 0 {
		for _, e := range sig.edges {
			if d := e.T - t; d >= -eps && d <= eps {
				boundary = true
				break
			}
		}
	}
	return bitV{v: v, ambig: boundary, t: t, ref: ref}, boundary
}

func decodeSPIInterval(bus *model.BusSpec, clk, mosi, miso *signal, csAlias string,
	iv [2]int64, res *model.Result) {

	modes := resolveModes(bus.SPIMode, clk.initial())
	var diags []model.Diagnostic
	if len(modes) == 0 {
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: "unsolvable", Bus: bus.Name, Channel: csAlias, TimeNs: iv[0],
			Message: "无法确定 SPI mode 且时钟空闲电平未知"})
		return
	}
	if len(modes) > 1 {
		diags = append(diags, model.Diagnostic{
			Code: "unknown_mode", Bus: bus.Name, Channel: csAlias, TimeNs: iv[0],
			Message: fmt.Sprintf("SPI mode 不明：保留 %d 个合法候选 %v", len(modes), modes)})
	}

	budget := budgetOf(bus)
	txnID := model.ShortID("txn", bus.Name, csAlias, fmt.Sprint(iv[0]))
	var cands []model.Candidate
	exhausted := false

	for _, m := range modes {
		cpha1 := m == 1 || m == 3
		var bitsM, bitsX []bitV
		anomalies := 0
		edgeIdx := 0
		for _, e := range clk.edges {
			if e.T < iv[0] || e.T > iv[1] {
				continue
			}
			isSample := edgeIdx%2 == 0
			if cpha1 {
				isSample = edgeIdx%2 == 1
			}
			edgeIdx++
			if !isSample {
				continue
			}
			bm, b1 := sampleAt(mosi, e.T, bus.BoundaryEpsilonNs)
			bx, b2 := sampleAt(miso, e.T, bus.BoundaryEpsilonNs)
			if b1 || b2 {
				anomalies++
				diags = append(diags, model.Diagnostic{
					Code: "boundary", Bus: bus.Name, Channel: csAlias, TimeNs: e.T,
					Message: fmt.Sprintf("边界采样：数据沿落在采样沿 ±%d ns 窗口内（mode %d）",
						bus.BoundaryEpsilonNs, m)})
			}
			bitsM = append(bitsM, bm)
			bitsX = append(bitsX, bx)
		}
		nbytes := len(bitsM) / 8
		if rem := len(bitsM) % 8; rem > 0 {
			diags = append(diags, model.Diagnostic{
				Code: "truncated", Bus: bus.Name, Channel: csAlias, TimeNs: iv[1],
				Message: fmt.Sprintf("截断：%d 个残缺位被丢弃（mode %d）", rem, m)})
			anomalies++
		}
		// 联合展开 MOSI/MISO 歧义位
		var flat []bitV
		for i := 0; i < nbytes*8; i++ {
			flat = append(flat, bitsM[i], bitsX[i])
		}
		remaining := budget - len(cands)
		if remaining <= 0 {
			exhausted = true
			break
		}
		combos, ex := forkCandidates(flat, 8, remaining, nil)
		if ex {
			exhausted = true
		}
		for _, combo := range combos {
			var bytes []model.Byte
			var hexParts string
			for bi := 0; bi < nbytes; bi++ {
				mb := combo[bi*16 : bi*16+8]
				xb := combo[bi*16+8 : bi*16+16]
				mv, xv := bitsValue(mb), bitsValue(xb)
				hexParts += fmt.Sprintf("%02x%02x;", mv, xv)
				bytes = append(bytes,
					spiByte(mosi, mb, mv, "mosi"),
					spiByte(miso, xb, xv, "miso"))
			}
			candID := model.ShortID("cand", txnID, fmt.Sprintf("mode%d", m), hexParts)
			for i := range bytes {
				bytes[i].ID = fmt.Sprintf("%s/b%d", candID, i)
			}
			reason := fmt.Sprintf("SPI mode %d", m)
			if len(modes) > 1 {
				reason = fmt.Sprintf("SPI mode 不明，候选 mode %d", m)
			}
			cands = append(cands, model.Candidate{
				ID: candID, Decoder: fmt.Sprintf("spi-mode%d", m),
				Params:     map[string]string{"mode": fmt.Sprint(m), "cs": csAlias},
				Bytes:      bytes,
				Confidence: confOf(anomalies),
				Anomalies:  anomalies,
				Reason:     reason,
			})
		}
	}
	if exhausted {
		diags = append(diags, model.Diagnostic{
			Code: "budget_exhausted", Bus: bus.Name, Channel: csAlias, TimeNs: iv[1],
			Message: fmt.Sprintf("资源预算耗尽：候选数超过预算 %d，仅保留确定序前 %d 个", budget, budget)})
	}
	if len(cands) > budget {
		cands = cands[:budget]
	}
	sortCandidates(cands)
	res.Transactions = append(res.Transactions, model.Transaction{
		ID: txnID, Bus: bus.Name, Protocol: "spi", Device: csAlias,
		StartNs: iv[0], EndNs: iv[1], Status: "ok",
		Candidates: cands, Diagnostics: diags,
	})
}

func spiByte(sig *signal, bits []bitV, value int, role string) model.Byte {
	amb := false
	var refs []model.EdgeRef
	for _, b := range bits {
		if b.ambig {
			amb = true
		}
		if b.ref.Channel != "" {
			refs = append(refs, b.ref)
		}
	}
	b := model.Byte{
		Value: value, Bits: 8, Role: role, Ambig: amb, EdgeRefs: refs,
	}
	if len(bits) > 0 {
		b.StartNs = bits[0].t
		b.EndNs = bits[len(bits)-1].t
	}
	return b
}

package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

type i2cEv struct {
	t   int64
	ch  int // 0=SDA 1=SCL
	lvl int
	idx int
}

// decodeI2C 事件驱动译码：重复 START、clock stretching、时钟缺边、
// 边界采样、数据缺口都形成结构化状态与候选。
func decodeI2C(bus *model.BusSpec, sigs map[string]*signal, res *model.Result) {
	sda := needChan(res, bus, sigs, bus.Data, "SDA")
	scl := needChan(res, bus, sigs, bus.Clock, "SCL")
	if sda == nil || scl == nil {
		return
	}

	var evs []i2cEv
	for i, e := range sda.edges {
		evs = append(evs, i2cEv{e.T, 0, e.Level, i})
	}
	for i, e := range scl.edges {
		evs = append(evs, i2cEv{e.T, 1, e.Level, i})
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].t < evs[j].t })

	sdaLvl, sclLvl := sda.initial(), scl.initial()
	inTxn := false
	var bits []bitV
	var segments [][]bitV
	var diags []model.Diagnostic
	anomalies := 0
	var txnStart, lastT, lastSCLFall int64
	sdaEdgesSinceRise := 0
	var sdaBuf []model.EdgeRef

	var txns []model.Transaction
	closeTxn := func(endT int64, status string) {
		segments = append(segments, bits)
		bits = nil
		txn := assembleI2CTxn(bus, sda, segments, txnStart, endT, status, diags, anomalies, res)
		if txn != nil {
			txns = append(txns, *txn)
		}
		segments = nil
		diags = nil
		anomalies = 0
	}

	for _, e := range evs {
		if inTxn && bus.GapThresholdNs > 0 && e.t-lastT > bus.GapThresholdNs {
			diags = append(diags, model.Diagnostic{
				Code: "gap", Bus: bus.Name, TimeNs: lastT,
				Message: fmt.Sprintf("数据缺口 %d ns 超过阈值 %d ns：交易在此截断，不拼接后续边沿",
					e.t-lastT, bus.GapThresholdNs)})
			closeTxn(lastT, "partial")
			inTxn = false
		}
		lastT = e.t
		if e.ch == 0 {
			prev := sdaLvl
			sdaLvl = e.lvl
			if sclLvl == 1 {
				switch {
				case prev == 1 && e.lvl == 0: // START
					if inTxn {
						diags = append(diags, model.Diagnostic{
							Code: "repeated_start", Bus: bus.Name, Channel: sda.alias,
							EdgeIdx: e.idx, TimeNs: e.t,
							Message: "重复 START：无 STOP 直接重启，地址阶段重置，交易保持连续"})
						segments = append(segments, bits)
						bits = nil
					} else {
						inTxn = true
						txnStart = e.t
					}
					sdaEdgesSinceRise = 0
					sdaBuf = nil
				case prev == 0 && e.lvl == 1 && inTxn: // STOP
					closeTxn(e.t, "ok")
					inTxn = false
				}
				continue
			}
			if inTxn {
				sdaEdgesSinceRise++
				sdaBuf = append(sdaBuf, model.EdgeRef{Channel: sda.alias, Index: e.idx, T: e.t})
			}
			continue
		}
		// SCL
		prev := sclLvl
		sclLvl = e.lvl
		if prev == 1 && e.lvl == 0 {
			lastSCLFall = e.t
			continue
		}
		if !(prev == 0 && e.lvl == 1) || !inTxn {
			continue
		}
		// SCL 上升沿：先查 clock stretching
		if bus.StretchThresholdNs > 0 && lastSCLFall > 0 && e.t-lastSCLFall > bus.StretchThresholdNs {
			diags = append(diags, model.Diagnostic{
				Code: "stretch", Bus: bus.Name, Channel: scl.alias, EdgeIdx: e.idx, TimeNs: lastSCLFall,
				Message: fmt.Sprintf("clock stretching：SCL 低电平保持 %d ns 超过阈值 %d ns",
					e.t-lastSCLFall, bus.StretchThresholdNs)})
		}
		// 时钟缺边：两个 SCL 上升沿之间出现多条 SDA 边沿
		for k := 0; k < sdaEdgesSinceRise-1; k++ {
			diags = append(diags, model.Diagnostic{
				Code: "missing_clock_edge", Bus: bus.Name, Channel: sda.alias,
				EdgeIdx: sdaBuf[k].Index, TimeNs: sdaBuf[k].T,
				Message: "时钟缺边：SDA 已翻转而 SCL 脉冲缺失，该位无法唯一决定，保留多个解码候选"})
			anomalies++
			bits = append(bits, bitV{v: 0, ambig: true, t: sdaBuf[k].T, ref: sdaBuf[k]})
		}
		sdaEdgesSinceRise = 0
		// 边界采样：SDA 建立时间不足
		amb := false
		var ref model.EdgeRef
		if len(sdaBuf) > 0 {
			ref = sdaBuf[len(sdaBuf)-1]
			if bus.BoundaryEpsilonNs > 0 && e.t-ref.T <= bus.BoundaryEpsilonNs {
				amb = true
				anomalies++
				diags = append(diags, model.Diagnostic{
					Code: "boundary", Bus: bus.Name, Channel: sda.alias,
					EdgeIdx: ref.Index, TimeNs: ref.T,
					Message: fmt.Sprintf("边界采样：SDA 在 SCL 上升沿前 %d ns 才变化（窗口 %d ns）",
						e.t-ref.T, bus.BoundaryEpsilonNs)})
			}
		} else if idx := sda.lastEdgeAtOrBefore(e.t); idx >= 0 {
			ref = model.EdgeRef{Channel: sda.alias, Index: idx, T: sda.edges[idx].T}
		}
		sdaBuf = nil
		bits = append(bits, bitV{v: sdaLvl, ambig: amb, t: e.t, ref: ref})
	}
	if inTxn {
		closeTxn(lastT, "partial")
	}
	res.Transactions = append(res.Transactions, txns...)
}

func assembleI2CTxn(bus *model.BusSpec, sda *signal, segments [][]bitV,
	startNs, endNs int64, status string, diags []model.Diagnostic, anomalies int,
	res *model.Result) *model.Transaction {

	var frames [][]bitV
	var roles []string
	for si, seg := range segments {
		full := len(seg) / 9
		rem := len(seg) % 9
		for f := 0; f < full; f++ {
			frames = append(frames, seg[f*9:(f+1)*9])
			roles = append(roles, i2cRole(f))
		}
		if rem == 8 {
			diags = append(diags, model.Diagnostic{
				Code: "missing_clock_edge", Bus: bus.Name, TimeNs: endNs,
				Message: "第 9 个时钟（ACK）缺边：ACK/NACK 无法唯一决定，保留多个解码候选"})
			anomalies++
			frame := make([]bitV, 9)
			copy(frame, seg[full*9:])
			frame[8] = bitV{v: 0, ambig: true, t: endNs}
			frames = append(frames, frame)
			roles = append(roles, i2cRole(full))
		} else if rem > 0 {
			diags = append(diags, model.Diagnostic{
				Code: "truncated", Bus: bus.Name, TimeNs: endNs,
				Message: fmt.Sprintf("截断：%d 个残缺位被丢弃，未凑成字节", rem)})
			anomalies++
		}
		_ = si
	}
	if len(frames) == 0 {
		return nil
	}

	var flat []bitV
	for _, f := range frames {
		flat = append(flat, f...)
	}
	budget := budgetOf(bus)
	combos, exhausted := forkCandidates(flat, 9, budget, nil)
	if exhausted {
		diags = append(diags, model.Diagnostic{
			Code: "budget_exhausted", Bus: bus.Name, TimeNs: endNs,
			Message: fmt.Sprintf("资源预算耗尽：歧义组合超过预算 %d，仅保留前 %d 个候选", budget, budget)})
	}

	txnID := model.ShortID("txn", bus.Name, fmt.Sprint(startNs))
	var cands []model.Candidate
	for ci, combo := range combos {
		var bytes []model.Byte
		var hexParts string
		pos := 0
		for fi := range frames {
			fb := combo[pos : pos+9]
			pos += 9
			value := bitsValue(fb[:8])
			ack := fb[8].v == 0
			amb := false
			var refs []model.EdgeRef
			for _, b := range frames[fi] {
				if b.ambig {
					amb = true
				}
				if b.ref.Channel != "" {
					refs = append(refs, b.ref)
				}
			}
			hexParts += fmt.Sprintf("%02x%t;", value, ack)
			bytes = append(bytes, model.Byte{
				Value: value, Bits: 8,
				StartNs: fb[0].t, EndNs: fb[8].t,
				Role: roles[fi], Ack: &ack, Ambig: amb,
				EdgeRefs: refs,
			})
		}
		candID := model.ShortID("cand", txnID, "i2c", hexParts)
		for i := range bytes {
			bytes[i].ID = fmt.Sprintf("%s/b%d", candID, i)
		}
		reason := "唯一位流"
		if len(combos) > 1 {
			reason = "时钟缺边/边界采样导致歧义，保留全部合法候选"
		}
		cands = append(cands, model.Candidate{
			ID: candID, Decoder: "i2c",
			Params:     map[string]string{"bus": bus.Name},
			Bytes:      bytes,
			Confidence: confOf(anomalies),
			Anomalies:  anomalies,
			Reason:     reason,
		})
		_ = ci
	}
	sortCandidates(cands)
	device := ""
	if len(cands) > 0 && len(cands[0].Bytes) > 0 && cands[0].Bytes[0].Role == "address" {
		device = fmt.Sprintf("0x%02X", cands[0].Bytes[0].Value>>1)
	}
	return &model.Transaction{
		ID: txnID, Bus: bus.Name, Protocol: "i2c", Device: device,
		StartNs: startNs, EndNs: endNs, Status: status,
		Candidates: cands, Diagnostics: diags,
	}
}

func i2cRole(frameIdx int) string {
	if frameIdx == 0 {
		return "address"
	}
	return "data"
}

package core

import (
	"fmt"
	"sort"
	"strings"
)

type i2cTx struct {
	startT  int64
	endT    int64
	bits    []Bit
	edgeIDs []string
	diags   []Diagnostic
	group   string
}

// DecodeI2C 在 SDA/SCL 边沿（已反相、已规范化）上译码 I2C。
// 规则：重复 START 沿同传输组开新段；clock stretching 记事件继续；
// 时钟缺边/边界采样保留多个候选；数据缺口直接截断、绝不拼接。
func DecodeI2C(edges []Edge, p I2CParams, budget *Budget) ([]Transaction, []Diagnostic) {
	type ev struct {
		e    Edge
		role string
	}
	var evs []ev
	var sdaTimes []int64
	for _, e := range edges {
		switch e.Ch {
		case "SDA":
			evs = append(evs, ev{e, "SDA"})
			sdaTimes = append(sdaTimes, e.TPS)
		case "SCL":
			evs = append(evs, ev{e, "SCL"})
		}
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].e.TPS != evs[j].e.TPS {
			return evs[i].e.TPS < evs[j].e.TPS
		}
		// 同一时刻先处理 SCL，再处理 SDA（稳定规则，文档化）。
		if evs[i].role != evs[j].role {
			return evs[i].role == "SCL"
		}
		return evs[i].e.Seq < evs[j].e.Seq
	})
	sort.Slice(sdaTimes, func(i, j int) bool { return sdaTimes[i] < sdaTimes[j] })

	var (
		txs         []Transaction
		diags       []Diagnostic
		sda, scl    = 1, 1
		open        *i2cTx
		groupSeq    int
		lastT       int64
		haveLastT   bool
		lastFall    int64
		haveFall    bool
		orphanBits  []Bit
		orphanE     []string
		orphanStart int64
	)
	warn := func(dst *[]Diagnostic, pos, code, msg string) {
		*dst = append(*dst, Diagnostic{Layer: "decode", Pos: pos, Severity: "warning", Code: code, Message: msg})
	}
	closeTx := func(reason string) {
		if open == nil {
			return
		}
		open.endT = lastT
		if reason == "repeated_start" {
			warn(&open.diags, open.group, "repeated_start", "检测到重复 START：不发送 STOP，沿同一传输组开启新段")
		}
		if tx := finalizeI2C(*open, p, budget); tx != nil {
			txs = append(txs, *tx)
		} else {
			diags = append(diags, open.diags...)
		}
		open = nil
	}
	flushOrphan := func() {
		if len(orphanBits) == 0 {
			return
		}
		id := ShortID("TX-", "i2c:orphan", strings.Join(orphanE, ","))
		dg := Diagnostic{Layer: "decode", Pos: id, Severity: "error", Code: "orphan_bits",
			Message: "出现无 START 界定的 SCL 采样位（常见于数据缺口之后）：按孤儿片段保留，不与任何交易拼接"}
		v := Variant{ID: id + ":V:orphan", Label: "orphan", Confidence: 0}
		for start := 0; start < len(orphanBits); start += 9 {
			end := start + 9
			if end > len(orphanBits) {
				end = len(orphanBits)
			}
			bc := i2cByte(len(v.Bytes), orphanBits[start:end])
			bc.ID = fmt.Sprintf("%s:B%d", v.ID, bc.Index)
			bc.Notes = append(bc.Notes, "orphan_after_gap")
			if len(orphanBits[start:end]) < 9 {
				bc.Notes = append(bc.Notes, "partial_word")
			}
			v.Bytes = append(v.Bytes, bc)
		}
		txs = append(txs, Transaction{
			ID: id, Protocol: "i2c", Status: StatusUnsolvable, StartPS: orphanStart,
			EndPS: lastT, Variants: []Variant{v}, Diagnostics: []Diagnostic{dg}, EdgeIDs: orphanE,
		})
		orphanBits, orphanE = nil, nil
	}

	for _, x := range evs {
		if haveLastT && open != nil && x.e.TPS-lastT > p.GapThresholdPS {
			open.endT = lastT
			warn(&open.diags, open.group, "data_gap",
				fmt.Sprintf("交易中途出现 %s 的边沿缺口：截断本交易，缺口后的边沿不拼入", FormatPS(x.e.TPS-lastT)))
			if tx := finalizeI2C(*open, p, budget); tx != nil {
				txs = append(txs, *tx)
			}
			open = nil
		}
		lastT = x.e.TPS
		haveLastT = true
		switch x.role {
		case "SCL":
			prev := scl
			isSample := (p.SampleEdge == "rising" && prev == 0 && x.e.Level == 1) ||
				(p.SampleEdge == "falling" && prev == 1 && x.e.Level == 0)
			if prev == 1 && x.e.Level == 0 {
				lastFall = x.e.TPS
				haveFall = true
			}
			if isSample && haveFall && x.e.TPS-lastFall > p.StretchThresholdPS {
				if open != nil {
					warn(&open.diags, x.e.ID, "clock_stretch",
						fmt.Sprintf("SCL 低电平持续 %s 超过阈值 %s（clock stretching），译码继续",
							FormatPS(x.e.TPS-lastFall), FormatPS(p.StretchThresholdPS)))
				}
			}
			if isSample {
				amb := false
				for _, t := range sdaTimes {
					d := t - x.e.TPS
					if d < 0 {
						d = -d
					}
					if d <= p.BoundaryWindowPS {
						amb = true
						break
					}
				}
				if open != nil {
					open.bits = append(open.bits, Bit{Value: sda, ClockEdgeID: x.e.ID, Ambiguous: amb})
					open.edgeIDs = append(open.edgeIDs, x.e.ID)
					if amb {
						warn(&open.diags, x.e.ID, "boundary_sample", "SDA 在采样窗内变化，该位保留 0/1 两个候选")
					}
				} else {
					if len(orphanBits) == 0 {
						orphanStart = x.e.TPS
					}
					orphanBits = append(orphanBits, Bit{Value: sda, ClockEdgeID: x.e.ID})
					orphanE = append(orphanE, x.e.ID)
				}
			}
			scl = x.e.Level
		case "SDA":
			if sda == 1 && x.e.Level == 0 && scl == 1 {
				if open != nil {
					closeTx("repeated_start")
				} else {
					flushOrphan()
					groupSeq++
				}
				open = &i2cTx{startT: x.e.TPS, group: fmt.Sprintf("I2C-G%02d", groupSeq)}
				open.edgeIDs = append(open.edgeIDs, x.e.ID)
			} else if sda == 0 && x.e.Level == 1 && scl == 1 && open != nil {
				open.edgeIDs = append(open.edgeIDs, x.e.ID)
				closeTx("stop")
			}
			sda = x.e.Level
		}
	}
	if open != nil {
		closeTx("eof")
	}
	flushOrphan()
	return txs, diags
}

func i2cByte(idx int, chunk []Bit) ByteCand {
	bc := ByteCand{Index: idx, Confidence: 1, Bits: append([]Bit(nil), chunk...)}
	for i := 0; i < len(chunk) && i < 8; i++ {
		bc.Value = (bc.Value << 1) | chunk[i].Value
	}
	if len(chunk) >= 9 {
		bc.HasAck = true
		bc.Ack = chunk[8].Value == 0
	}
	for _, bit := range chunk {
		if bit.ClockEdgeID != "" {
			bc.EdgeIDs = append(bc.EdgeIDs, bit.ClockEdgeID)
		}
	}
	return bc
}

func finalizeI2C(b i2cTx, p I2CParams, budget *Budget) *Transaction {
	if len(b.bits) == 0 && len(b.edgeIDs) == 0 {
		return nil
	}
	var opts [][]byteOpt
	var extra []Diagnostic
	for start := 0; start < len(b.bits); start += 9 {
		end := start + 9
		if end > len(b.bits) {
			end = len(b.bits)
		}
		chunk := b.bits[start:end]
		idx := start / 9
		ambIdx := -1
		for i, bit := range chunk {
			if bit.Ambiguous {
				ambIdx = i
			}
		}
		if len(chunk) == 9 && ambIdx < 0 {
			opts = append(opts, []byteOpt{{cand: i2cByte(idx, chunk), weight: 1}})
			continue
		}
		var cands []byteOpt
		mk := func(value int, hasAck, ack bool, weight float64, tag, note string) {
			bc := i2cByte(idx, chunk)
			bc.Value = value
			bc.HasAck = hasAck
			bc.Ack = ack
			bc.Confidence = weight
			bc.Notes = append(bc.Notes, note)
			cands = append(cands, byteOpt{cand: bc, tag: tag, weight: weight})
		}
		pos := fmt.Sprintf("%s:B%d", b.group, idx)
		switch {
		case len(chunk) < 9:
			extra = append(extra, Diagnostic{Layer: "decode", Pos: pos, Severity: "warning", Code: "missing_clock",
				Message: fmt.Sprintf("第 %d 字节仅观测到 %d/9 个 SCL 采样沿：时钟缺边，保留全部合法候选", idx+1, len(chunk))})
			val := 0
			for i := 0; i < len(chunk) && i < 8; i++ {
				val = (val << 1) | chunk[i].Value
			}
			if len(chunk) >= 8 {
				mk(val, true, false, 0.5, "ack=0", "missing_clock:ack")
				mk(val, true, true, 0.5, "ack=1", "missing_clock:ack")
			} else {
				miss := 8 - len(chunk)
				mk(val<<miss, false, false, 0.5, "fill=0", "missing_clock:data")
				mk(val<<miss|(1<<miss)-1, false, false, 0.5, "fill=1", "missing_clock:data")
			}
		default: // 9 位齐全但含边界采样二义位
			base := 0
			for i := 0; i < 8; i++ {
				base = (base << 1) | chunk[i].Value
			}
			ack := chunk[8].Value == 0
			v0 := base &^ (1 << (7 - ambIdx))
			v1 := v0 | 1<<(7-ambIdx)
			extra = append(extra, Diagnostic{Layer: "decode", Pos: pos, Severity: "warning", Code: "boundary_sample",
				Message: fmt.Sprintf("第 %d 字节第 %d 位位于采样边界，保留 0x%02X/0x%02X 两个候选", idx+1, ambIdx+1, v0, v1)})
			mk(v0, true, ack, 0.5, fmt.Sprintf("b%d=0", ambIdx), "boundary_sample")
			mk(v1, true, ack, 0.5, fmt.Sprintf("b%d=1", ambIdx), "boundary_sample")
		}
		opts = append(opts, cands)
	}
	id := ShortID("TX-", "i2c", strings.Join(b.edgeIDs, ","), p.SampleEdge, p.BoundaryWindowPS, p.GapThresholdPS)
	variants, blown := buildVariants(id, opts, budget)
	diags := append(append([]Diagnostic{}, b.diags...), extra...)
	if blown {
		diags = append(diags, Diagnostic{Layer: "decode", Pos: id, Severity: "error", Code: "budget_exhausted",
			Message: "候选数量超过资源预算：保留已生成候选并标记预算耗尽"})
	}
	if len(variants) == 0 {
		variants = []Variant{{ID: id + ":V:base", Label: "base", Confidence: 1}}
	}
	status := StatusOK
	switch {
	case blown:
		status = StatusBudgetExhausted
	case len(variants) > 1:
		status = StatusAmbiguous
	}
	tx := &Transaction{
		ID: id, Protocol: "i2c", GroupID: b.group, StartPS: b.startT, EndPS: b.endT,
		Status: status, Variants: variants, Diagnostics: diags, EdgeIDs: b.edgeIDs,
	}
	if len(variants) > 0 && len(variants[0].Bytes) > 0 {
		addr := variants[0].Bytes[0].Value >> 1
		read := variants[0].Bytes[0].Value&1 == 1
		tx.Address = &addr
		tx.Read = &read
	}
	return tx
}

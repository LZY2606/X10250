package decode

import (
	"sort"

	"edgebench/internal/model"
)

type ambiguity struct {
	kind string
	t    int64
	edge int
	opts [2]string
}

type resolver map[int]int

type coreOut struct {
	txns  []model.Transaction
	evs   []model.DecodeEvent
	ambs  []ambiguity
	diags []model.Diagnostic
}

// i2cCore 以给定歧义解析方式重组一次 I2C 捕获。
func i2cCore(scl, sda []cedge, gapFactor, stretchFactor int, res resolver) coreOut {
	var out coreOut
	riseSCL := rises(scl)
	if len(riseSCL) < 2 {
		return out
	}
	half := medianInterval(scl)
	if half <= 0 {
		return out
	}
	period := 2 * half

	type mEv struct {
		t    int64
		kind int // 0=sdaFall 1=sdaRise 2=sclRise
		e    cedge
	}
	merged := make([]mEv, 0, len(sda)+len(riseSCL))
	for _, e := range sda {
		k := 1
		if e.level == 0 {
			k = 0
		}
		merged = append(merged, mEv{t: e.t, kind: k, e: e})
	}
	for _, e := range riseSCL {
		merged = append(merged, mEv{t: e.t, kind: 2, e: e})
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].t != merged[j].t {
			return merged[i].t < merged[j].t
		}
		return merged[i].kind < merged[j].kind
	})

	inFrame := false
	var cur *model.Transaction
	var bits []int
	var bitEdges []int
	var bitTimes []int64
	byteCount := 0
	lastRise := int64(-1)
	endTime := merged[len(merged)-1].t

	finish := func(status string, endNs int64) {
		if cur == nil {
			inFrame = false
			bits, bitEdges, bitTimes = nil, nil, nil
			return
		}
		cur.Status = status
		cur.EndNs = endNs
		if status != "ok" && status != "repeated_start" {
			cur.Invalid = "帧未以 STOP 结束：" + status
		}
		out.txns = append(out.txns, *cur)
		cur = nil
		inFrame = false
		bits, bitEdges, bitTimes = nil, nil, nil
	}

	emitByte := func(endNs int64) {
		val := 0
		for _, b := range bits[:8] {
			val = val<<1 | b
		}
		ack := "nack"
		if bits[8] == 0 {
			ack = "ack"
		}
		cur.Bytes = append(cur.Bytes, model.Byte{
			Index: byteCount, Value: val, Ack: ack, CS: -1,
			StartEdge: bitEdges[0], EndEdge: bitEdges[8],
			StartNs: bitTimes[0], EndNs: endNs,
		})
		byteCount++
		bits, bitEdges, bitTimes = nil, nil, nil
	}

	pushBit := func(bit, edgeIdx int, t int64) {
		bits = append(bits, bit)
		bitEdges = append(bitEdges, edgeIdx)
		bitTimes = append(bitTimes, t)
		if len(bits) == 9 && cur != nil {
			emitByte(t)
		}
	}

	for _, me := range merged {
		switch me.kind {
		case 0: // SDA falling
			if levelAt(scl, me.t) == 1 {
				if inFrame {
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "repeated_start", EdgeIndex: me.e.idx, TimeNs: me.t,
						Detail: "SCL 高电平时 SDA 下降沿出现于未结束帧内，按重复 START 分段",
					})
					finish("repeated_start", me.t)
				}
				cur = &model.Transaction{Kind: "i2c_frame", CS: -1, StartNs: me.t}
				inFrame = true
			}
		case 1: // SDA rising
			if inFrame && levelAt(scl, me.t) == 1 {
				finish("ok", me.t)
			}
		case 2: // SCL rising = 采样点
			if !inFrame {
				lastRise = me.t
				continue
			}
			if lastRise >= 0 {
				dt := me.t - lastRise
				switch {
				case dt >= int64(gapFactor)*period:
					// 数据缺口：不拼接成同一交易。
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "data_gap", EdgeIndex: me.e.idx, TimeNs: lastRise,
						Detail: gapDetail(dt, period, gapFactor),
					})
					finish("gap_terminated", lastRise)
					lastRise = me.t
					continue
				case dt >= int64(stretchFactor)*period:
					// clock stretching：从机保持 SCL 低，继续采样并留证。
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "clock_stretch", EdgeIndex: me.e.idx, TimeNs: lastRise,
						Detail: gapDetail(dt, period, stretchFactor),
					})
				case dt*2 >= 3*period:
					// 缺边：一个 SCL 脉冲疑似缺失，无法唯一决定，保留全部合法候选。
					ambIdx := len(out.ambs)
					out.ambs = append(out.ambs, ambiguity{
						kind: "missing_clock_edge", t: me.t, edge: me.e.idx,
						opts: [2]string{"insert_virtual_bit", "end_transfer"},
					})
					opt := res[ambIdx]
					out.evs = append(out.evs, model.DecodeEvent{
						Type: "missing_clock_edge", EdgeIndex: me.e.idx, TimeNs: me.t,
						Detail: gapDetail(dt, period, 2) + "；选择=" + out.ambs[ambIdx].opts[opt],
					})
					if opt == 1 {
						finish("open", lastRise)
						lastRise = me.t
						continue
					}
					// 在中点插入一个虚拟采样位。
					pushBit(levelAt(sda, lastRise+period), me.e.idx, lastRise+period)
				}
			}
			pushBit(levelAt(sda, me.t), me.e.idx, me.t)
			lastRise = me.t
		}
	}
	if inFrame {
		finish("open", endTime)
	}
	return out
}

func gapDetail(dt, period int64, factor int) string {
	return mustJSON(map[string]any{
		"interval_ns": dt, "period_ns": period, "threshold_factor": factor,
		"ratio": float64(dt) / float64(period),
	})
}

func decodeI2C(m *model.Material, p model.DecodeParams, prep *prepared) decoderOutput {
	gapFactor, stretchFactor := p.GapFactor, p.StretchFactor
	if gapFactor <= 0 {
		gapFactor = 8
	}
	if stretchFactor <= 0 {
		stretchFactor = 3
	}
	need := map[string]bool{"scl": true, "sda": true}
	diags := missingRoleDiag(need, m, prep)
	if len(diags) > 0 {
		return decoderOutput{diags: diags, unsolvable: true}
	}
	base := i2cCore(prep.byRole["scl"], prep.byRole["sda"], gapFactor, stretchFactor, resolver{})
	if len(base.evs) == 0 && len(base.txns) == 0 {
		// 有 SCL 但无法定位任何 START，语义上无法重组出交易。
		base.diags = append(base.diags, model.Diagnostic{
			Code: "no_start_condition", Message: "未观察到 SCL 高电平时的 SDA 下降沿，无法定位任何 I2C 交易",
			EdgeIndex: -1, Channel: -1,
		})
		return decoderOutput{diags: base.diags, unsolvable: true}
	}
	out := decoderOutput{diags: base.diags}
	out.specs = append(out.specs, candidateSpec{
		key: "base", label: "基线：缺边插入虚拟采样位", penalty: 0,
		txns: base.txns, events: withKept(base.evs, len(base.ambs)),
	})
	// 每个缺边点单独翻转一次：只改动一个直接来源，便于核对影响范围。
	for i, a := range base.ambs {
		if len(out.specs) >= 8 {
			base.evs = append(base.evs, model.DecodeEvent{
				Type: "candidate_cap", TimeNs: a.t, EdgeIndex: a.edge,
				Detail: "并列候选达到 8 个上限，剩余翻转未展开（资源预算）",
			})
			break
		}
		res := resolver{i: 1}
		alt := i2cCore(prep.byRole["scl"], prep.byRole["sda"], gapFactor, stretchFactor, res)
		key := "missing_clock_edge@" + model.Digest("amb", i, a.t, a.edge)[:8] + "=end_transfer"
		out.specs = append(out.specs, candidateSpec{
			key: key,
			label: "候选：缺边处结束当前传输（" + a.opts[1] + " @ " + nsLabel(a.t) + "）",
			penalty: 0.2, txns: alt.txns,
			events: withKept(alt.evs, 1),
		})
	}
	return out
}

func withKept(evs []model.DecodeEvent, altCount int) []model.DecodeEvent {
	out := append([]model.DecodeEvent{}, evs...)
	out = append(out, model.DecodeEvent{
		Type:   "parallel_candidates_kept",
		Detail: mustJSON(map[string]any{"alternate_candidates": altCount}),
	})
	return out
}

func nsLabel(t int64) string {
	return formatNs(t)
}

package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/domain"
)

// I2C 解码：START/STOP 条件、位采样、ACK/NACK、clock stretching、
// 时钟缺边（partial byte）、边界采样歧义、重复 START、数据缺口。
func I2C(c *domain.Capture, p domain.ParamSet) (*domain.Result, *domain.DiagError) {
	sdaCh := c.ChannelByRole("sda")
	sclCh := c.ChannelByRole("scl")
	if len(sdaCh) == 0 || len(sclCh) == 0 {
		return nil, &domain.DiagError{Kind: "semantic-unsolvable", Diags: []domain.Diag{{
			Kind: "semantic-unsolvable", Context: c.Name,
			Message: "I2C 解码需要 sda 与 scl 通道，采集声明不完整",
		}}}
	}
	sda := c.EdgesOf(sdaCh[0].ID)
	scl := c.EdgesOf(sclCh[0].ID)
	W := p.I2C.BoundaryAmbigNs

	sclTr := transitions(scl, 1)
	sdaTr := transitions(sda, 1)

	// 事件流：SCL 上升沿采样点 + SDA 起始/停止条件 + 缺口边界
	type ev struct {
		t     int64
		kind  string // sample / start / stop / gap
		tr    transition
		gap   domain.Gap
		order int
	}
	var evs []ev
	for _, tr := range sclTr {
		if tr.Rise {
			evs = append(evs, ev{t: tr.Edge.T, kind: "sample", tr: tr})
		}
	}
	for _, tr := range sdaTr {
		sclLvl := levelAt(scl, tr.Edge.T, 1)
		if sclLvl == 1 {
			if tr.Fall {
				evs = append(evs, ev{t: tr.Edge.T, kind: "start", tr: tr})
			} else if tr.Rise {
				evs = append(evs, ev{t: tr.Edge.T, kind: "stop", tr: tr})
			}
		}
	}
	for _, g := range c.Gaps {
		evs = append(evs, ev{t: g.Start, kind: "gap", gap: g})
	}
	for i := range evs {
		evs[i].order = i
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		// 同一时刻：条件先于采样（START/STOP 由 SDA 在 SCL 高时变化产生）
		rank := func(k string) int {
			switch k {
			case "start", "stop":
				return 0
			case "gap":
				return 1
			}
			return 2
		}
		if rank(evs[i].kind) != rank(evs[j].kind) {
			return rank(evs[i].kind) < rank(evs[j].kind)
		}
		return evs[i].order < evs[j].order
	})

	// clock stretching 标记（相邻 SCL 边沿间隔超过阈值）
	var stretchMarkers []domain.Marker
	for i := 1; i < len(sclTr); i++ {
		d := sclTr[i].Edge.T - sclTr[i-1].Edge.T
		if p.I2C.StretchTimeoutNs > 0 && d > p.I2C.StretchTimeoutNs {
			stretchMarkers = append(stretchMarkers, domain.Marker{
				Kind: "stretch", T: sclTr[i-1].Edge.T, TEnd: sclTr[i].Edge.T,
				Note: fmt.Sprintf("clock stretching: SCL 保持 %d%s 超过阈值 %d%s", d, c.TimeUnit, p.I2C.StretchTimeoutNs, c.TimeUnit),
			})
		}
	}

	// 逐事件构建段（segment）
	type seg struct {
		bits      []domain.Bit
		ambigIdx  []int // 边界采样歧义的位下标
		markers   []domain.Marker
		start     int64
		gapSplit  bool
		repeated  bool
		open      bool
		byteStart int64
	}
	var segs []*seg
	cur := &seg{}
	closeSeg := func() {
		if cur.open || len(cur.bits) > 0 {
			segs = append(segs, cur)
		}
		cur = &seg{}
	}
	var lastSampleT int64 = -1
	for _, e := range evs {
		switch e.kind {
		case "start":
			if cur.open || len(cur.bits) > 0 {
				cur.markers = append(cur.markers, domain.Marker{Kind: "repeated-start", T: e.t, Note: "重复 START：无 STOP 直接重新开始"})
				closeSeg()
				cur.repeated = true
			}
			cur.open = true
			cur.start = e.t
			cur.byteStart = e.t
			cur.markers = append(cur.markers, domain.Marker{Kind: "start", T: e.t})
		case "stop":
			cur.markers = append(cur.markers, domain.Marker{Kind: "stop", T: e.t})
			closeSeg()
		case "gap":
			if cur.open || len(cur.bits) > 0 {
				cur.markers = append(cur.markers, domain.Marker{Kind: "gap", T: e.gap.Start, TEnd: e.gap.End, Note: "数据缺口：" + e.gap.Note})
				closeSeg()
				cur.gapSplit = true // 缺口之后另起段，绝不跨缺口拼接
			}
		case "sample":
			if lastSampleT >= 0 {
				if g, crossed := gapBetween(c, lastSampleT, e.t); crossed {
					cur.markers = append(cur.markers, domain.Marker{Kind: "gap", T: g.Start, TEnd: g.End, Note: "数据缺口：" + g.Note})
					closeSeg()
					cur.gapSplit = true
				}
			}
			lastSampleT = e.t
			v := levelAt(sda, e.t, 1)
			var de string
			if le := lastEdgeAtOrBefore(sda, e.t); le != nil {
				de = le.ID
			}
			bit := domain.Bit{Value: v, T: e.t, EdgeID: e.tr.Edge.ID, DataEdge: de}
			// 边界采样歧义：采样点附近存在 SDA 边沿
			if W > 0 && sdaEdgeNear(sda, e.t, W) {
				cur.ambigIdx = append(cur.ambigIdx, len(cur.bits))
				cur.markers = append(cur.markers, domain.Marker{Kind: "boundary", T: e.t, Note: fmt.Sprintf("边界采样：采样点 ±%d%s 内存在 SDA 边沿", W, c.TimeUnit)})
			}
			if !cur.open && len(cur.bits) == 0 {
				cur.start = e.t
				cur.byteStart = e.t
			}
			cur.bits = append(cur.bits, bit)
		}
	}
	closeSeg()

	// 段 -> 交易（基础候选）
	buildTx := func(s *seg, dropPartial bool, txIndex *int) ([]domain.Transaction, []domain.Marker) {
		var txs []domain.Transaction
		var markers []domain.Marker
		bits := s.bits
		nFull := len(bits) / 9
		rem := len(bits) % 9
		var bytes []domain.Byte
		for i := 0; i < nFull; i++ {
			grp := bits[i*9 : i*9+9]
			dataBits := grp[:8]
			ackBit := grp[8]
			ack := ackBit.Value == 0
			b := domain.Byte{
				Index: i,
				Value: assembleByte(dataBits, "msb", 8),
				Bits:  dataBits,
				Ack:   &ack,
				AckT:  ackBit.T,
				Start: dataBits[0].T,
				End:   ackBit.T,
			}
			b.Hex = fmt.Sprintf("0x%02X", b.Value)
			bytes = append(bytes, b)
			markers = append(markers, domain.Marker{Kind: ackKind(ack), T: ackBit.T})
		}
		if rem > 0 {
			grp := bits[nFull*9:]
			m := domain.Marker{Kind: "missing-edge", T: grp[0].T,
				Note: fmt.Sprintf("时钟缺边：段尾仅剩 %d 位，不足完整字节+ACK", len(grp))}
			markers = append(markers, m)
			if !dropPartial && p.I2C.AllowPartialByte {
				pb := domain.Byte{
					Index:   nFull,
					Value:   assembleByte(grp, "msb", 8),
					Bits:    grp,
					Start:   grp[0].T,
					End:     grp[len(grp)-1].T,
					Partial: true,
				}
				pb.Hex = fmt.Sprintf("0x%02X?", pb.Value)
				bytes = append(bytes, pb)
			}
		}
		if len(bytes) > 0 {
			tx := domain.Transaction{
				Kind:  "data",
				Bytes: bytes, Start: s.start, End: bytes[len(bytes)-1].End,
				GapSplit: s.gapSplit,
			}
			if !s.gapSplit && len(bytes) > 0 && !bytes[0].Partial {
				tx.Kind = "address+data"
				tx.Address = bytes[0].Value >> 1
				if bytes[0].Value&1 == 1 {
					tx.RW = "read"
				} else {
					tx.RW = "write"
				}
			}
			tx.Index = *txIndex
			*txIndex++
			txs = append(txs, tx)
		}
		return txs, markers
	}

	// 基础候选：丢弃残缺字节，重复 START 拆分为独立交易
	txIdx := 0
	var baseTxs []domain.Transaction
	var baseMarkers []domain.Marker
	baseMarkers = append(baseMarkers, stretchMarkers...)
	for _, s := range segs {
		txs, ms := buildTx(s, true, &txIdx)
		baseTxs = append(baseTxs, txs...)
		baseMarkers = append(baseMarkers, ms...)
		baseMarkers = append(baseMarkers, s.markers...)
	}
	sortMarkers(baseMarkers)

	base := domain.Candidate{
		Rank: 0, Label: "基础解码", Confidence: 0.9, Adopted: true,
		Assumption:   "重复 START 拆分为独立交易；残缺字节丢弃",
		Transactions: baseTxs, Markers: baseMarkers,
	}
	cands := []domain.Candidate{base}

	// 备选 1：边界采样歧义位翻转
	hasAmbig := false
	for _, s := range segs {
		if len(s.ambigIdx) > 0 {
			hasAmbig = true
		}
	}
	if hasAmbig {
		txIdx2 := 0
		var altTxs []domain.Transaction
		var altMarkers []domain.Marker
		for _, s := range segs {
			alt := *s
			alt.bits = make([]domain.Bit, len(s.bits))
			copy(alt.bits, s.bits)
			for _, bi := range s.ambigIdx {
				if bi < len(alt.bits) {
					alt.bits[bi].Value = 1 - alt.bits[bi].Value
				}
			}
			txs, ms := buildTx(&alt, true, &txIdx2)
			altTxs = append(altTxs, txs...)
			altMarkers = append(altMarkers, ms...)
		}
		cands = append(cands, domain.Candidate{
			Rank: 1, Label: "边界采样备选", Confidence: 0.45, Adopted: false,
			Assumption:   "边界采样歧义位取反后的解码",
			Transactions: altTxs, Markers: altMarkers,
		})
	}

	// 备选 2：重复 START 合并为复合交易
	hasRepeated := false
	for _, s := range segs {
		if s.repeated {
			hasRepeated = true
		}
	}
	if hasRepeated {
		var merged []domain.Transaction
		for _, tx := range baseTxs {
			if len(merged) > 0 && !tx.GapSplit {
				prev := &merged[len(merged)-1]
				prev.Bytes = append(prev.Bytes, tx.Bytes...)
				prev.End = tx.End
				prev.Kind = "compound(repeated-start)"
				continue
			}
			merged = append(merged, tx)
		}
		for i := range merged {
			merged[i].Index = i
		}
		cands = append(cands, domain.Candidate{
			Rank: len(cands), Label: "重复 START 合并", Confidence: 0.6, Adopted: false,
			Assumption:   "重复 START 视为同一复合交易的段分隔",
			Transactions: merged, Markers: baseMarkers,
		})
	}

	// 备选 3：保留残缺字节（时钟缺边）
	hasPartial := false
	for _, s := range segs {
		if len(s.bits)%9 != 0 && len(s.bits) > 0 {
			hasPartial = true
		}
	}
	if hasPartial && p.I2C.AllowPartialByte {
		txIdx3 := 0
		var altTxs []domain.Transaction
		var altMarkers []domain.Marker
		for _, s := range segs {
			txs, ms := buildTx(s, false, &txIdx3)
			altTxs = append(altTxs, txs...)
			altMarkers = append(altMarkers, ms...)
		}
		cands = append(cands, domain.Candidate{
			Rank: len(cands), Label: "保留残缺字节", Confidence: 0.4, Adopted: false,
			Assumption:   "时钟缺边导致的残缺字节按部分字节保留",
			Transactions: altTxs, Markers: altMarkers,
		})
	}

	res := &domain.Result{
		Proto:      "i2c",
		DecoderVer: domain.DecoderVersionI2C,
		CaptureID:  c.ID,
		ParamID:    p.ID,
		InputHash:  inputHash(c, p.ID, domain.DecoderVersionI2C, "i2c"),
		Candidates: cands,
	}
	res.ID = domain.ComputeResultID(res.Proto, res.DecoderVer, res.CaptureID, res.ParamID, res.InputHash, candidateKey(cands))
	return res, nil
}

func ackKind(ack bool) string {
	if ack {
		return "ack"
	}
	return "nack"
}

func sdaEdgeNear(sda []domain.Edge, t, w int64) bool {
	for _, e := range sda {
		d := e.T - t
		if d < 0 {
			d = -d
		}
		if d <= w {
			return true
		}
	}
	return false
}

func sortMarkers(ms []domain.Marker) {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].T < ms[j].T })
}

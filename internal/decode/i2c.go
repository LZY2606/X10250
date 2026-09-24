package decode

import (
	"fmt"
	"sort"

	"edgeworkbench/internal/model"
)

// i2cState is one decode hypothesis. Ambiguity forks clone it.
type i2cState struct {
	conf      float64
	rationale []string
	diags     []model.Diag
	txns      []model.Transaction
	open      *model.Transaction
	bits      []int
	bitStart  []int64
	bitEnd    []int64
	bitEdges  []int
	lastAct   int64
	sawStart  bool
}

func (s *i2cState) clone() *i2cState {
	c := *s
	c.rationale = append([]string(nil), s.rationale...)
	c.diags = append([]model.Diag(nil), s.diags...)
	c.txns = append([]model.Transaction(nil), s.txns...)
	c.bits = append([]int(nil), s.bits...)
	c.bitStart = append([]int64(nil), s.bitStart...)
	c.bitEnd = append([]int64(nil), s.bitEnd...)
	c.bitEdges = append([]int(nil), s.bitEdges...)
	if s.open != nil {
		t := *s.open
		t.Bytes = append([]model.Byte(nil), s.open.Bytes...)
		c.open = &t
	}
	return &c
}

func (s *i2cState) closeTxn(status, reason string, endNs int64) {
	if s.open == nil {
		return
	}
	s.open.EndNs = endNs
	s.open.Status = status
	s.open.InvalidReason = reason
	s.txns = append(s.txns, *s.open)
	s.open = nil
	s.bits = nil
	s.bitStart = nil
	s.bitEnd = nil
	s.bitEdges = nil
}

func (s *i2cState) emitByte(p *model.ParamSet) {
	if len(s.bits) < 9 || s.open == nil {
		return
	}
	v := 0
	for i := 0; i < 8; i++ {
		v = v<<1 | s.bits[i]
	}
	ack := "NACK"
	if s.bits[8] == 0 {
		ack = "ACK"
	}
	refs := append([]int(nil), s.bitEdges...)
	s.open.Bytes = append(s.open.Bytes, model.Byte{
		Seq: len(s.open.Bytes), Value: v, Ack: ack,
		StartNs: s.bitStart[0], EndNs: s.bitEnd[8],
		EdgeRefs: refs, Confidence: s.conf,
	})
	s.bits = s.bits[:0]
	s.bitStart = s.bitStart[:0]
	s.bitEnd = s.bitEnd[:0]
	s.bitEdges = s.bitEdges[:0]
}

func decodeI2C(c *model.Capture, p *model.ParamSet, b *budget) []model.Candidate {
	sclCh := findRole(c, model.RoleSCL)[0]
	sdaCh := findRole(c, model.RoleSDA)[0]
	sclE, sclI := channelEdges(c, sclCh.Index)
	sdaE, sdaI := channelEdges(c, sdaCh.Index)
	scl := newStream(c, sclCh, sclE, sclI)
	sda := newStream(c, sdaCh, sdaE, sdaI)

	if len(sclE) == 0 {
		return []model.Candidate{{
			Protocol: "i2c", Confidence: 0,
			Rationale: []string{"SCL 通道没有任何边沿，无法解码"},
			Diags: []model.Diag{{
				Severity: "error", Code: model.DiagUnsolvable,
				Message: "SCL 通道无边沿", Channel: sclCh.Index, TimeNs: -1, EdgeIdx: -1,
			}},
		}}
	}

	// nominal half period: median of consecutive SCL edge deltas
	var deltas []int64
	for i := 1; i < len(sclE); i++ {
		d := sclE[i].TimeNs - sclE[i-1].TimeNs
		if d > 0 {
			deltas = append(deltas, d)
		}
	}
	sort.Slice(deltas, func(i, j int) bool { return deltas[i] < deltas[j] })
	half := deltas[len(deltas)/2]
	if half <= 0 {
		half = 1
	}

	type ev struct {
		t     int64
		isSCL bool
		level int
		idx   int
	}
	var evs []ev
	for i, e := range sclE {
		lv := e.Level
		if sclCh.Invert {
			lv ^= 1
		}
		evs = append(evs, ev{e.TimeNs, true, lv, sclI[i]})
	}
	for i, e := range sdaE {
		lv := e.Level
		if sdaCh.Invert {
			lv ^= 1
		}
		evs = append(evs, ev{e.TimeNs, false, lv, sdaI[i]})
	}
	sort.SliceStable(evs, func(i, j int) bool {
		if evs[i].t != evs[j].t {
			return evs[i].t < evs[j].t
		}
		return evs[i].idx < evs[j].idx
	})

	states := []*i2cState{{conf: 1.0, rationale: []string{
		fmt.Sprintf("SCL 标称半周期 %dns（中位数）", half),
	}}}
	var lastRise int64 = -1
	var fellAt int64 = -1

	for _, e := range evs {
		// data gap: idle longer than threshold with an open transaction and
		// no STOP -> terminate, never stitch across the gap.
		for _, s := range states {
			if s.open != nil && e.t-s.lastAct > p.I2C.GapThresholdNs {
				reason := fmt.Sprintf("数据缺口: %dns 无活动且无 STOP，交易不拼接", e.t-s.lastAct)
				s.diags = append(s.diags, model.Diag{
					Severity: "warning", Code: model.DiagDataGap, Message: reason,
					Channel: -1, TimeNs: s.lastAct, EdgeIdx: -1,
				})
				s.closeTxn(model.TxnGapTerminated, reason, s.lastAct)
			}
		}

		if !e.isSCL {
			// SDA edge: START/STOP only while SCL high
			if scl.levelAt(e.t) != 1 {
				for _, s := range states {
					s.lastAct = e.t
				}
				continue
			}
			if e.level == 0 { // START or repeated START
				for _, s := range states {
					if s.open != nil && len(s.open.Bytes) > 0 {
						s.diags = append(s.diags, model.Diag{
							Severity: "info", Code: model.DiagRepeatedStart,
							Message: "重复 START：未发 STOP 直接重启交易",
							Channel: sdaCh.Index, TimeNs: e.t, EdgeIdx: e.idx,
						})
						s.closeTxn(model.TxnOK, "terminated_by_repeated_start", e.t)
					} else if s.open != nil {
						s.closeTxn(model.TxnIncomplete, "empty_transaction_restarted", e.t)
					}
					s.open = &model.Transaction{
						Kind: "i2c_write", StartNs: e.t, Status: model.TxnIncomplete,
					}
					s.sawStart = true
					s.lastAct = e.t
				}
			} else { // STOP
				for _, s := range states {
					if s.open != nil {
						s.emitByte(p)
						s.closeTxn(model.TxnOK, "", e.t)
					}
					s.lastAct = e.t
				}
			}
			continue
		}

		// SCL edge
		if e.level == 0 {
			fellAt = e.t
			for _, s := range states {
				s.lastAct = e.t
			}
			continue
		}
		// SCL rising
		var next []*i2cState
		for _, s := range states {
			if fellAt >= 0 && e.t-fellAt > p.I2C.StretchThresholdNs {
				s.diags = append(s.diags, model.Diag{
					Severity: "warning", Code: model.DiagClockStretch,
					Message: fmt.Sprintf("clock stretching: SCL 低电平保持 %dns 超过阈值 %dns",
						e.t-fellAt, p.I2C.StretchThresholdNs),
					Channel: sclCh.Index, TimeNs: fellAt, EdgeIdx: e.idx,
				})
			}
			// missing clock: rising-to-rising gap too large and SDA moved
			if lastRise >= 0 && e.t-lastRise > 2*half+half/2 && s.open != nil {
				moved := false
				for i, se := range sdaE {
					if se.TimeNs > lastRise && se.TimeNs < e.t && scl.levelAt(se.TimeNs) == 0 {
						_ = i
						moved = true
						break
					}
				}
				if moved && p.I2C.MaxMissedClocks > 0 {
					s.diags = append(s.diags, model.Diag{
						Severity: "warning", Code: model.DiagClockMissing,
						Message: fmt.Sprintf("时钟缺边：两个 SCL 上升沿间隔 %dns（标称 %dns），期间 SDA 有变化",
							e.t-lastRise, 2*half),
						Channel: sclCh.Index, TimeNs: lastRise, EdgeIdx: e.idx,
					})
					// fork: hypothesis A = one missed clock (sample mid-gap level),
					// hypothesis B = no missed clock (long stretch)
					if b.fork(1) {
						alt := s.clone()
						alt.conf *= 0.5
						mid := (lastRise + e.t) / 2
						alt.bits = append(alt.bits, sda.levelAt(mid))
						alt.bitStart = append(alt.bitStart, mid)
						alt.bitEnd = append(alt.bitEnd, mid)
						alt.bitEdges = append(alt.bitEdges, e.idx)
						alt.rationale = append(alt.rationale,
							fmt.Sprintf("假设缺 1 个时钟边沿，在 %dns 补采样", mid))
						alt.diags = append(alt.diags, model.Diag{
							Severity: "info", Code: model.DiagFork,
							Message: "缺边分叉：补采样假设", Channel: sclCh.Index,
							TimeNs: mid, EdgeIdx: e.idx,
						})
						next = append(next, alt)
						s.rationale = append(s.rationale, "假设无缺边（长拉伸）")
					}
				}
			}
			// boundary sampling: SDA edge within setup margin of this rising edge
			bit := sda.levelAt(e.t)
			for i, se := range sdaE {
				d := e.t - se.TimeNs
				if d < 0 {
					d = -d
				}
				if d <= p.I2C.SetupMarginNs && scl.levelAt(se.TimeNs) == 0 {
					if b.fork(1) {
						alt := s.clone()
						alt.conf *= 0.5
						alt.bits = append(alt.bits, bit^1)
						alt.bitStart = append(alt.bitStart, e.t)
						alt.bitEnd = append(alt.bitEnd, e.t)
						alt.bitEdges = append(alt.bitEdges, e.idx, sdaI[i])
						alt.rationale = append(alt.rationale,
							fmt.Sprintf("边界采样分叉：SDA 在 %dns 距 SCL 上升沿 %dns，取反值", se.TimeNs, d))
						alt.diags = append(alt.diags, model.Diag{
							Severity: "info", Code: model.DiagBoundarySample,
							Message: fmt.Sprintf("边界采样：SDA 边沿距采样点 %dns", d),
							Channel: sdaCh.Index, TimeNs: se.TimeNs, EdgeIdx: sdaI[i],
						})
						next = append(next, alt)
					}
					s.diags = append(s.diags, model.Diag{
						Severity: "warning", Code: model.DiagBoundarySample,
						Message: fmt.Sprintf("边界采样：SDA 边沿距 SCL 上升沿 %dns（余量 %dns）", d, p.I2C.SetupMarginNs),
						Channel: sdaCh.Index, TimeNs: se.TimeNs, EdgeIdx: sdaI[i],
					})
					break
				}
			}
			if s.open != nil {
				s.bits = append(s.bits, bit)
				s.bitStart = append(s.bitStart, e.t)
				s.bitEnd = append(s.bitEnd, e.t)
				s.bitEdges = append(s.bitEdges, e.idx)
				s.emitByte(p)
			}
			s.lastAct = e.t
			next = append(next, s)
		}
		states = next
		lastRise = e.t
	}

	var out []model.Candidate
	for _, s := range states {
		if s.open != nil {
			s.emitByte(p)
			s.closeTxn(model.TxnIncomplete, "capture_ended_without_stop", s.lastAct)
		}
		// classify transactions: first byte is address (R/W bit)
		for i := range s.txns {
			t := &s.txns[i]
			t.Seq = i
			if len(t.Bytes) > 0 {
				t.Addr = t.Bytes[0].Value >> 1
				if t.Bytes[0].Value&1 == 1 {
					t.Kind = "i2c_read"
				}
			}
		}
		if len(s.txns) == 0 && len(s.diags) == 0 {
			continue
		}
		out = append(out, model.Candidate{
			Protocol: "i2c", Confidence: s.conf,
			Rationale: s.rationale, Transactions: s.txns, Diags: s.diags,
		})
	}
	return out
}

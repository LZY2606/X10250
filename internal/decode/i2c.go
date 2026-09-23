package decode

import (
	"fmt"

	"edgeworkbench/internal/model"
)

type i2cState struct {
	txns       []model.Transaction
	cur        *model.Transaction
	bits       []int
	bitEdges   []string
	byteStarts []int64
	sclLevel   int
	sdaLevel   int
	sclFellAt  int64
	lastEdgeT  int64
	inTxn      bool
	expectAddr bool
	pendBit    bool
	reason     string
	confidence float64
}

func (s *i2cState) clone() *i2cState {
	c := *s
	c.bits = append([]int(nil), s.bits...)
	c.bitEdges = append([]string(nil), s.bitEdges...)
	c.byteStarts = append([]int64(nil), s.byteStarts...)
	c.txns = append([]model.Transaction(nil), s.txns...)
	if s.cur != nil {
		t := *s.cur
		t.Bytes = append([]model.Byte(nil), s.cur.Bytes...)
		t.Flags = append([]string(nil), s.cur.Flags...)
		c.cur = &t
	}
	return &c
}

func decodeI2C(c *model.Capture, p *model.Params, budget *candidateBudget) *busResult {
	sclCh, okSCL := chanByRole(c, "scl")
	sdaCh, okSDA := chanByRole(c, "sda")
	if !okSCL || !okSDA {
		return nil
	}
	res := &busResult{}
	col := &collector{seen: map[string]bool{}}

	var edges []model.Edge
	for _, e := range c.Edges {
		if e.Channel == sclCh.Alias || e.Channel == sdaCh.Alias {
			edges = append(edges, e)
		}
	}
	type group struct {
		t     int64
		edges []model.Edge
	}
	var groups []group
	for _, e := range edges {
		if n := len(groups); n > 0 && groups[n-1].t == e.TimeNs {
			groups[n-1].edges = append(groups[n-1].edges, e)
		} else {
			groups = append(groups, group{t: e.TimeNs, edges: []model.Edge{e}})
		}
	}

	states := []*i2cState{{sclLevel: 1, sdaLevel: 1, reason: "primary", confidence: 1.0}}
	orphanClocks := 0
	var orphanAt int64

	for _, g := range groups {
		t := g.t
		var sclRise, sclFall, sdaRise, sdaFall bool
		var sclRiseEdge model.Edge
		sdaNewLevel := -1
		for _, e := range g.edges {
			if e.Channel == sclCh.Alias {
				if levelOf(sclCh, e.Level) == 1 {
					sclRise = true
					sclRiseEdge = e
				} else {
					sclFall = true
				}
			} else {
				sdaNewLevel = levelOf(sdaCh, e.Level)
				if sdaNewLevel == 1 {
					sdaRise = true
				} else {
					sdaFall = true
				}
			}
		}

		n := len(states) // forks created in this group join from the next group
		for si := 0; si < n; si++ {
			st := states[si]
			prevEdgeT := st.lastEdgeT
			st.lastEdgeT = t

			// data gap: terminate, never stitch
			if st.inTxn && t-prevEdgeT > p.GapThresholdNs {
				st.handleMissingClock(t, col, &states, budget, func(x *i2cState) {
					x.closeTxn(t, model.EvDataGapNoStitch, col)
				})
				if !st.inTxn {
					// closeTxn ran inside handleMissingClock only when
					// dangling bits existed; close plainly otherwise
				}
			}

			boundary := sclRise && sdaNewLevel >= 0 && st.inTxn
			if boundary {
				col.add(model.EvBoundarySample, sdaCh.Alias, t,
					fmt.Sprintf("SDA toggles exactly at SCL rising edge t=%dns; sampled bit is ambiguous, keeping both candidates", t))
				budget.fork(1)
				if !budget.exceeded() {
					nb := st.clone()
					nb.reason = "boundary_sample"
					nb.confidence = 0.5
					nb.sclLevel = 1
					nb.sdaLevel = sdaNewLevel
					nb.sampleBitWith(t, sclRiseEdge.ID, sdaNewLevel)
					nb.pendBit = true
					states = append(states, nb)
				}
				st.sampleBitWith(t, sclRiseEdge.ID, st.sdaLevel)
				st.pendBit = true
				st.sclLevel = 1
				st.sdaLevel = sdaNewLevel
				continue
			}

			// START / STOP (SDA moves while SCL high; a simultaneous
			// SCL fall means this is a data-bit change, not a condition)
			if sdaFall && st.sclLevel == 1 && !sclFall {
				st.dropPendingBit()
				if st.inTxn {
					col.add(model.EvRepeatedStart, sdaCh.Alias, t,
						fmt.Sprintf("SDA falls while SCL high at t=%dns inside an open transaction: repeated START", t))
					st.flagCur(model.EvRepeatedStart)
					st.handleMissingClock(t, col, &states, budget, func(x *i2cState) {
						x.bits, x.bitEdges = nil, nil
						x.expectAddr = true
					})
				} else {
					st.cur = &model.Transaction{Bus: "i2c", StartNs: t}
					st.inTxn = true
					st.expectAddr = true
					st.bits, st.bitEdges = nil, nil
				}
			}
			if sdaRise && st.sclLevel == 1 && !sclFall && st.inTxn {
				st.dropPendingBit()
				st.handleMissingClock(t, col, &states, budget, func(x *i2cState) {
					x.closeTxn(t, "", col)
				})
			}

			if sclRise {
				if st.sclLevel == 0 && t-st.sclFellAt > p.StretchThresholdNs && st.inTxn {
					col.add(model.EvClockStretch, sclCh.Alias, t,
						fmt.Sprintf("SCL held low for %dns (> threshold %dns) before t=%dns: clock stretching", t-st.sclFellAt, p.StretchThresholdNs, t))
					st.flagCur(model.EvClockStretch)
				}
				st.sclLevel = 1
				if st.inTxn {
					st.sampleBit(t, sclRiseEdge.ID)
					st.pendBit = true
				} else {
					orphanClocks++
					orphanAt = t
				}
			}
			if sclFall {
				st.sclLevel = 0
				st.sclFellAt = t
				st.pendBit = false
			}
			if sdaRise {
				st.sdaLevel = 1
			}
			if sdaFall {
				st.sdaLevel = 0
			}
		}
	}
	for _, st := range states {
		if st.inTxn {
			st.closeTxn(st.lastEdgeT, model.EvDataGapNoStitch, col)
		}
	}
	if orphanClocks > 0 {
		res.diags = append(res.diags, model.Diagnostic{
			Category: model.DiagSemantic,
			Channel:  sclCh.Alias,
			TimeNs:   orphanAt,
			Position: fmt.Sprintf("channel:%s t=%dns", sclCh.Alias, orphanAt),
			Message:  fmt.Sprintf("%d SCL clock edges occur outside any START..STOP frame (e.g. after a data gap); region is semantically unsolvable and left undecoded", orphanClocks),
		})
	}

	for _, st := range states {
		if len(st.txns) == 0 {
			continue
		}
		conf := st.confidence
		if len(states) > 1 && st.reason == "primary" {
			conf = 0.5
		}
		res.candidates = append(res.candidates, model.Candidate{
			Bus: "i2c", Reason: st.reason, Confidence: conf,
			Transactions: st.txns,
		})
	}
	res.events = col.events
	return res
}

func (s *i2cState) sampleBit(t int64, edgeID string) {
	s.sampleBitWith(t, edgeID, s.sdaLevel)
}

func (s *i2cState) sampleBitWith(t int64, edgeID string, bit int) {
	s.bits = append(s.bits, bit)
	s.bitEdges = append(s.bitEdges, edgeID)
	if len(s.bits)%9 == 1 {
		s.byteStarts = append(s.byteStarts, t)
	}
	if len(s.bits)%9 == 0 {
		s.emitByte(t)
	}
}

func (s *i2cState) emitByte(t int64) {
	n := len(s.bits)
	b := s.bits[n-9 : n-1]
	ackBit := s.bits[n-1]
	val := 0
	for _, bit := range b {
		val = val<<1 | bit
	}
	ack := "NACK"
	if ackBit == 0 {
		ack = "ACK"
	}
	by := model.Byte{
		Index:   len(s.cur.Bytes),
		Value:   val,
		Ack:     ack,
		StartNs: s.byteStartNs(n),
		EndNs:   t,
		EdgeIDs: append([]string(nil), s.bitEdges[n-9:n]...),
	}
	if s.expectAddr {
		s.cur.Address = val >> 1
		s.cur.Read = val&1 == 1
		s.expectAddr = false
	}
	s.cur.Bytes = append(s.cur.Bytes, by)
}

func (s *i2cState) byteStartNs(n int) int64 {
	idx := (n / 9) - 1
	if idx >= 0 && idx < len(s.byteStarts) {
		return s.byteStarts[idx]
	}
	return 0
}

// dropPendingBit discards a bit sampled at a SCL rise that turned out to be
// part of a START/STOP condition (no SCL fall intervened).
func (s *i2cState) dropPendingBit() {
	if s.pendBit && len(s.bits) > 0 {
		s.bits = s.bits[:len(s.bits)-1]
		s.bitEdges = s.bitEdges[:len(s.bitEdges)-1]
		if len(s.bits)%9 == 0 && len(s.byteStarts) > len(s.bits)/9 {
			s.byteStarts = s.byteStarts[:len(s.byteStarts)-1]
		}
	}
	s.pendBit = false
}

// handleMissingClock fires when a byte boundary arrives with dangling bits.
// The primary hypothesis truncates them; a zero-pad hypothesis is forked.
// The after callback (close transaction, reset for repeated START, ...) is
// applied to both hypotheses so their state machines stay in step.
func (s *i2cState) handleMissingClock(t int64, col *collector, states *[]*i2cState, budget *candidateBudget, after func(*i2cState)) {
	if len(s.bits)%9 == 0 || len(s.bits) == 0 {
		if after != nil {
			after(s)
		}
		return
	}
	col.add(model.EvClockEdgeMissing, "SCL", t,
		fmt.Sprintf("byte boundary reached at t=%dns with %d dangling bits (not a multiple of 9): clock edge missing; keeping truncate and zero-pad candidates", t, len(s.bits)%9))
	budget.fork(1)
	if !budget.exceeded() {
		nb := s.clone()
		nb.reason = "clock_edge_missing"
		nb.confidence = 0.4
		for len(nb.bits)%9 != 0 {
			nb.bits = append(nb.bits, 0)
			nb.bitEdges = append(nb.bitEdges, "pad")
		}
		nb.emitByte(t)
		if nb.cur != nil && len(nb.cur.Bytes) > 0 {
			nb.cur.Bytes[len(nb.cur.Bytes)-1].Ambiguous = true
		}
		nb.bits, nb.bitEdges = nil, nil
		if after != nil {
			after(nb)
		}
		*states = append(*states, nb)
	}
	s.bits, s.bitEdges = nil, nil
	if after != nil {
		after(s)
	}
}

func (s *i2cState) flagCur(flag string) {
	if s.cur == nil {
		return
	}
	for _, f := range s.cur.Flags {
		if f == flag {
			return
		}
	}
	s.cur.Flags = append(s.cur.Flags, flag)
}

func (s *i2cState) closeTxn(t int64, gapFlag string, col *collector) {
	if s.cur == nil {
		s.inTxn = false
		return
	}
	if gapFlag != "" {
		col.add(model.EvDataGapNoStitch, "SDA", t,
			fmt.Sprintf("no bus edges for longer than the gap threshold at t=%dns: transaction closed, later edges are NOT stitched into it", t))
		s.flagCur(model.EvDataGapNoStitch)
	}
	s.cur.EndNs = t
	s.txns = append(s.txns, *s.cur)
	s.cur = nil
	s.inTxn = false
	s.bits, s.bitEdges = nil, nil
}

type collector struct {
	events []evtDraft
	seen   map[string]bool
}

func (c *collector) add(kind, channel string, t int64, rationale string) {
	key := fmt.Sprintf("%s|%s|%d", kind, channel, t)
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.events = append(c.events, evtDraft{Kind: kind, Channel: channel, TimeNs: t, Rationale: rationale})
}

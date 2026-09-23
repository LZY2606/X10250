package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// I2C reassembly.
//
// Rules (decoder 1.0.0):
//   * START = SDA 1->0 while SCL is 1; STOP = SDA 0->1 while SCL is 1.
//   * A second START before a STOP is evidence EB-I2C-REPEATED-START and starts
//     a new transaction; it never merges the surrounding traffic.
//   * Bits are sampled on the active SCL edge (rising, unless the revisable
//     SCL polarity parameter flips it).
//   * If a data edge coincides with the sampling edge, BOTH boundary samples
//     are retained (confidence 0.5) instead of silently choosing one.
//   * A low SCL interval longer than the stretch threshold is evidence
//     EB-I2C-CLOCK-STRETCHING.
//   * A data gap longer than the bus gap threshold splits transactions
//     (EB-I2C-DATA-GAP); the gap is never bridged.
//   * A byte with fewer than 8 bits because the clock stops is not finished:
//     EB-I2C-MISSING-CLOCK plus every legal completion candidate.
type i2cState struct {
	c        *model.Capture
	bus      model.BusDef
	tl       map[string]*pinTimeline
	scl      *pinTimeline
	sda      *pinTimeline
	br       *model.BusReport
	pol      int
	gap      int64
	stretch  int64
	txs      []*model.Transaction
	cur      *model.Transaction
	bitAcc   []i2cBit // bits of the byte currently assembling
	prevSCLE *model.Edge
	started  bool
}

type i2cBit struct {
	// pre/post are the SDA physical levels around the sampling instant.
	pre, post  int
	coincident bool
	line       int // SDA source line when coincident, else SCL line
	t          int64
}

func decodeI2C(c *model.Capture, p *model.Params, bus model.BusDef, tl map[string]*pinTimeline, br *model.BusReport) {
	st := &i2cState{
		c: c, bus: bus, tl: tl, br: br,
		scl:     tl[bus.Pins["scl"]],
		sda:     tl[bus.Pins["sda"]],
		pol:     polFor(p, bus.Name),
		gap:     EffectiveGap(c, p, bus.Name),
		stretch: effectiveStretch(c),
	}
	br.Effective = model.BusEffective{ClockPol: st.pol, GapNS: st.gap, StretchNS: st.stretch, SPIMode: "-"}

	// Ordered union of SCL/SDA edges drives the state machine.
	ev := mergeEdges(st.scl.Edges, st.sda.Edges)
	lastActiveT := int64(-1)
	for _, e := range ev {
		if lastActiveT >= 0 && e.TNS-lastActiveT > st.gap && st.started {
			st.splitGap(e.TNS, lastActiveT)
		}
		isSCL := e.Pin == st.scl.Pin
		if isSCL {
			st.onSCLEdge(&e, &lastActiveT)
		} else {
			st.onSDAEdge(&e, &lastActiveT)
		}
	}
	st.finishTrailing()
	for i, tx := range st.txs {
		tx.Index = i
		br.Transactions = append(br.Transactions, tx)
	}
}

func polFor(p *model.Params, bus string) int {
	if v, ok := p.I2CPol[bus]; ok && v == -1 {
		return -1
	}
	return 1
}

func mergeEdges(a, b []model.Edge) []model.Edge {
	out := make([]model.Edge, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TNS != out[j].TNS {
			return out[i].TNS < out[j].TNS
		}
		if out[i].Pin != out[j].Pin {
			return out[i].Pin < out[j].Pin
		}
		return out[i].LineNo < out[j].LineNo
	})
	return out
}

func (s *i2cState) onSDAEdge(e *model.Edge, lastActive *int64) {
	sclLvl, _ := s.scl.levelAt(e.TNS)
	if sclLvl != 1 {
		*lastActive = e.TNS
		return
	}
	preLvl := 0
	// pre-edge SDA level
	if lvl, ln := s.levelBefore(s.sda, e.TNS); ln >= 0 {
		preLvl = lvl
	}
	switch {
	case preLvl == 1 && e.Level == 0 && s.started:
		s.evRepeatedStart(e)
		s.resetByte()
		*lastActive = e.TNS
	case preLvl == 1 && e.Level == 0:
		s.started = true
		s.cur = s.newTx(e.TNS)
		s.br.Evidence = append(s.br.Evidence, model.Evidence{
			Code: "EB-I2C-START", TNS: e.TNS, Severity: model.SevInfo, Bus: s.bus.Name,
			Message: "START condition", SourceLines: []int{e.LineNo},
		})
		*lastActive = e.TNS
	case preLvl == 0 && e.Level == 1 && s.started:
		s.finishByteAt(e.TNS)
		if s.cur != nil {
			s.cur.Complete = true
			s.cur.EndTNS = e.TNS
		}
		s.txs = append(s.txs, s.cur)
		s.cur = nil
		s.started = false
		s.br.Evidence = append(s.br.Evidence, model.Evidence{
			Code: "EB-I2C-STOP", TNS: e.TNS, Severity: model.SevInfo, Bus: s.bus.Name,
			Message: "STOP condition", SourceLines: []int{e.LineNo},
		})
		*lastActive = e.TNS
	}
}

func (s *i2cState) onSCLEdge(e *model.Edge, lastActive *int64) {
	if !s.started {
		return
	}
	active := (s.pol == 1 && e.Level == 1) || (s.pol == -1 && e.Level == 0)
	if s.prevSCLE != nil {
		low := e.TNS - s.prevSCLE.TNS
		// stretch measured between consecutive SCL edges while SCL held low
		// (physical), regardless of active polarity.
		if s.prevSCLE.Level == 0 && e.Level == 1 && low > s.stretch {
			s.br.Evidence = append(s.br.Evidence, model.Evidence{
				Code: "EB-I2C-CLOCK-STRETCHING", TNS: s.prevSCLE.TNS, EndTNS: e.TNS,
				Severity: model.SevWarning, Bus: s.bus.Name,
				Message: fmt.Sprintf("SCL held low %dns > stretch threshold %dns", low, s.stretch),
				SourceLines: []int{s.prevSCLE.LineNo, e.LineNo},
				Data: map[string]string{"low_ns": fmt.Sprint(low), "threshold_ns": fmt.Sprint(s.stretch)},
			})
		}
	}
	s.prevSCLE = &model.Edge{ID: e.ID, LineNo: e.LineNo, Pin: e.Pin, TNS: e.TNS, Level: e.Level}
	if !active {
		*lastActive = e.TNS
		return
	}
	pre, post, line, coinc := s.sda.levelAtCoincident(e.TNS)
	_ = post
	if coinc {
		s.bitAcc = append(s.bitAcc, i2cBit{pre: pre, post: post, coincident: true, line: line, t: e.TNS})
	} else {
		lvl, ln := s.sda.levelAt(e.TNS)
		s.bitAcc = append(s.bitAcc, i2cBit{pre: lvl, post: lvl, line: ln, t: e.TNS})
	}
	*lastActive = e.TNS
}

func (s *i2cState) resetByte() { s.bitAcc = nil }

// finishByteAt closes the currently assembled byte using up to 9 active edges
// (8 data + ACK/NACK). It is called on STOP; normally bytes close when the 9th
// active edge arrives (handled in pushBit).
func (s *i2cState) finishByteAt(stopT int64) {
	s.closeByte(stopT, true)
}

func (s *i2cState) splitGap(now, last int64) {
	s.br.Evidence = append(s.br.Evidence, model.Evidence{
		Code: "EB-I2C-DATA-GAP", TNS: last, EndTNS: now, Severity: model.SevWarning,
		Bus: s.bus.Name,
		Message: fmt.Sprintf("data gap %dns > threshold %dns; transactions are not joined", now-last, s.gap),
		Data:    map[string]string{"gap_ns": fmt.Sprint(now - last), "threshold_ns": fmt.Sprint(s.gap)},
	})
	s.closeByte(now, false)
	if s.cur != nil {
		s.cur.Complete = false
		s.txs = append(s.txs, s.cur)
		s.cur = nil
	}
	s.started = false
	s.resetByte()
}

func (s *i2cState) evRepeatedStart(e *model.Edge) {
	s.br.Evidence = append(s.br.Evidence, model.Evidence{
		Code: "EB-I2C-REPEATED-START", TNS: e.TNS, Severity: model.SevInfo, Bus: s.bus.Name,
		Message: "repeated START: previous transaction closes, a new one begins",
		SourceLines: []int{e.LineNo},
	})
	s.closeByte(e.TNS, false)
	if s.cur != nil {
		s.cur.EndTNS = e.TNS
		s.txs = append(s.txs, s.cur)
	}
	s.cur = s.newTx(e.TNS)
}

func (s *i2cState) newTx(t int64) *model.Transaction {
	return &model.Transaction{Kind: "i2c-tx", Bus: s.bus.Name, StartTNS: t, EndTNS: t}
}

// finishTrailing handles bytes left when a capture ends without STOP.
func (s *i2cState) finishTrailing() {
	if !s.started || s.cur == nil {
		return
	}
	s.closeByte(0, false)
	s.txs = append(s.txs, s.cur)
	s.cur = nil
	s.started = false
}

// pushBit is invoked after every active SCL edge; every 9th edge closes a byte.
func (s *i2cState) pushBit() {
	if len(s.bitAcc) < 9 {
		return
	}
	bits := s.bitAcc[:9]
	s.bitAcc = append([]i2cBit{}, s.bitAcc[9:]...)
	s.emitByte(bits, false)
}

// closeByte finalizes whatever is currently buffered; on STOP a complete
// 9-edge group closes normally, otherwise a short group is a missing clock.
func (s *i2cState) closeByte(_ int64, onStop bool) {
	if len(s.bitAcc) == 0 {
		return
	}
	if len(s.bitAcc) >= 9 {
		group := s.bitAcc[:9]
		s.bitAcc = append([]i2cBit{}, s.bitAcc[9:]...)
		s.emitByte(group, false)
		// any remainder before STOP is a trailing incomplete group
		if onStop && len(s.bitAcc) > 0 {
			s.emitByte(s.bitAcc, true)
		} else if len(s.bitAcc) > 0 {
			s.emitByte(s.bitAcc, true)
		}
		return
	}
	s.emitByte(s.bitAcc, true)
	s.bitAcc = nil
}

func (s *i2cState) emitByte(bits []i2cBit, incomplete bool) {
	if s.cur == nil {
		return
	}
	dataBits := bits
	ackBit := i2cBit{}
	hasACK := len(bits) >= 9
	if hasACK {
		dataBits = bits[:8]
		ackBit = bits[8]
	}
	idx := len(s.cur.Bytes)
	role := "data"
	if idx == 0 && hasACK {
		role = "address"
	}
	b := &model.Byte{
		Bus: s.bus.Name, TxIndex: len(s.txs), Index: idx, Role: role,
		TNS: dataBits[0].t,
	}
	lines := map[int]bool{}
	for _, bt := range dataBits {
		if bt.line >= 0 {
			lines[bt.line] = true
		}
	}
	b.SourceLines = sortedLines(lines)
	for _, ln := range b.SourceLines {
		b.EdgeIDs = append(b.EdgeIDs, fmt.Sprintf("%s/e/%d", s.c.ID, ln))
	}
	cands := i2cCandidates(dataBits)
	for i := range cands {
		cands[i].ID = fmt.Sprintf("c/i2c/%s/%d/%d/%s", s.bus.Name, b.TxIndex, idx, cands[i].ClockMode)
	}
	b.Candidates = cands
	if hasACK {
		b.ACKState = ackLabel(ackBit)
		// ACK/NACK rendered as its own reviewable slot carrying the 9th edge.
		s.attachACKSlot(ackBit, idx)
	}
	if incomplete {
		got := len(dataBits)
		b.Diags = append(b.Diags, model.Diagnostic{
			Severity: model.SevError, Code: "EB-I2C-MISSING-CLOCK", Bus: s.bus.Name,
			Message: fmt.Sprintf("missing SCL edge: byte assembled from %d of 8 data edges", got),
		})
		s.br.Evidence = append(s.br.Evidence, model.Evidence{
			Code: "EB-I2C-MISSING-CLOCK", TNS: dataBits[len(dataBits)-1].t,
			Severity: model.SevError, Bus: s.bus.Name,
			Message: fmt.Sprintf("clock stopped after %d data bit(s); all legal completions retained", got),
			SourceLines: linesSlice(dataBits),
			Data: map[string]string{"bits": fmt.Sprint(got), "need": "8"},
		})
	}
	s.cur.Bytes = append(s.cur.Bytes, b)
}

func (s *i2cState) attachACKSlot(ack i2cBit, dataIdx int) {
	slot := &model.Byte{
		Bus: s.bus.Name, TxIndex: len(s.txs), Index: dataIdx + 1, Role: "ack",
		TNS: ack.t,
	}
	if ack.line >= 0 {
		slot.SourceLines = []int{ack.line}
		slot.EdgeIDs = []string{fmt.Sprintf("%s/e/%d", s.c.ID, ack.line)}
	}
	label := ackLabel(ack)
	val := 0
	if label == "nack" {
		val = 1
	}
	conf := 1.0
	reason := "sampled ACK/NACK level on 9th active SCL edge"
	if ack.coincident {
		conf, reason = 0.5, "boundary sample: SDA edge coincides with 9th SCL edge"
	}
	slot.ACKState = label
	slot.Candidates = []*model.Candidate{{
		Value: val, ValueText: map[bool]string{true: "ACK", false: "NACK"}[label == "ack"],
		Bits:       bitS(val, 1),
		Confidence: conf, Reason: reason, ClockMode: fmt.Sprintf("pol%+d", s.pol), Preferred: true,
	}}
	s.cur.Bytes = append(s.cur.Bytes, slot)
}

func ackLabel(b i2cBit) string {
	if b.coincident {
		if b.pre != b.post {
			return "ambiguous"
		}
	}
	lvl := b.pre
	if lvl == 0 {
		return "ack"
	}
	return "nack"
}

// i2cCandidates expands every legal reading of a byte. Coincident edges
// contribute two readings; when several exist the legal set is capped
// deterministically at MaxCandidates.
func i2cCandidates(bits []i2cBit) []*model.Candidate {
	choices := make([][2]int, len(bits))
	amb := 0
	for i, b := range bits {
		choices[i] = [2]int{b.pre, b.post}
		if b.coincident && b.pre != b.post {
			amb++
		}
	}
	total := 1
	for _, ch := range choices {
		if ch[0] != ch[1] {
			total *= 2
		}
	}
	var vals []int
	var expand func(pos, acc int)
	expand = func(pos, acc int) {
		if len(vals) >= MaxCandidates {
			return
		}
		if pos == len(choices) {
			vals = append(vals, acc)
			return
		}
		ch := choices[pos]
		expand(pos+1, (acc<<1)|ch[0])
		if ch[0] != ch[1] {
			expand(pos+1, (acc<<1)|ch[1])
		}
	}
	expand(0, 0)
	// stable order
	seen := map[int]bool{}
	var out []*model.Candidate
	for _, v := range vals {
		if seen[v] {
			continue
		}
		seen[v] = true
		conf := 1.0
		reason := "all 8 data edges sampled unambiguously"
		if amb > 0 {
			conf = 1.0 / float64(min2(total, 1<<amb))
			reason = fmt.Sprintf("%d boundary sample(s): SDA edge coincides with SCL active edge", amb)
		}
		out = append(out, &model.Candidate{
			Value: v, ValueText: fmt.Sprintf("0x%02X", v), Bits: bitS(v, len(bits)),
			Confidence: round2(conf), Reason: reason,
		})
	}
	if len(out) == 0 {
		out = append(out, &model.Candidate{Value: 0, ValueText: "0x??", Bits: "", Confidence: 0, Reason: "no samples"})
	}
	// preferred: highest value confidence, deterministic smallest value on ties
	best := 0
	for i := range out {
		if out[i].Confidence > out[best].Confidence ||
			(out[i].Confidence == out[best].Confidence && out[i].Value < out[best].Value) {
			best = i
		}
	}
	for i := range out {
		out[i].Preferred = i == best
		out[i].ClockMode = "i2c"
	}
	return out
}

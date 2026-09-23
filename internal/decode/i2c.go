package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

type i2cBit struct {
	val      int
	t        int64
	edgeRefs []int
}

type i2cState struct {
	cap  *model.Capture
	p    model.Params
	ov   map[string]bool
	txs  []model.Transaction
	tx   *model.Transaction
	bits []i2cBit
	ambs []ambiguity
	diag []model.Diagnostic

	sclLvl, sdaLvl   int
	lastEdgeT        int64
	sclFellT         int64
	sdaSinceRise     []edgeAt
	addrPhase        bool
	byteCount        int
	seq              int
}

func ambKey(reason string, t int64) string { return fmt.Sprintf("%s@%d", reason, t) }

func (s *i2cState) useAlt(reason string, t int64) bool { return s.ov[ambKey(reason, t)] }

func (s *i2cState) closeTx(flag string) {
	if s.tx == nil {
		return
	}
	if len(s.bits) > 0 {
		s.diag = append(s.diag, model.Diagnostic{
			Code: "I2C_PARTIAL_BYTE", Severity: "warning", TimeNs: s.lastEdgeT, EdgeIndex: -1,
			Message: fmt.Sprintf("transaction closed with %d dangling bits", len(s.bits)),
		})
		s.bits = nil
	}
	if flag != "" {
		s.tx.Flags = append(s.tx.Flags, flag)
	}
	s.tx.EndNs = s.lastEdgeT
	s.txs = append(s.txs, *s.tx)
	s.tx = nil
}

func (s *i2cState) openTx(t int64, repeated bool) {
	s.tx = &model.Transaction{
		Protocol: "i2c",
		StartNs:  t, EndNs: t,
	}
	s.addrPhase = true
	s.bits = nil
	if repeated {
		s.tx.Flags = append(s.tx.Flags, "repeated_start")
	}
}

func (s *i2cState) pushBit(val int, t int64, refs ...int) {
	s.bits = append(s.bits, i2cBit{val: val, t: t, edgeRefs: refs})
	if len(s.bits) == 9 {
		v := 0
		var refs []int
		var start int64
		for i := 0; i < 8; i++ {
			v = v<<1 | s.bits[i].val
			refs = append(refs, s.bits[i].edgeRefs...)
		}
		refs = append(refs, s.bits[8].edgeRefs...)
		start = s.bits[0].t
		ack := "nack"
		if s.bits[8].val == 0 {
			ack = "ack"
		}
		b := model.Byte{
			Value: v, Bits: 8, Ack: ack,
			StartNs: start, EndNs: s.bits[8].t, EdgeRefs: refs,
		}
		if s.addrPhase {
			b.IsAddress = true
			b.Address = v >> 1
			if v&1 == 1 {
				b.ReadWrite = "read"
			} else {
				b.ReadWrite = "write"
			}
			s.tx.Device = fmt.Sprintf("0x%02X", b.Address)
			s.addrPhase = false
		}
		b.ID = fmt.Sprintf("i2c-b-%d-%02x-%d", start, v, s.byteCount)
		s.byteCount++
		s.tx.Bytes = append(s.tx.Bytes, b)
		s.tx.EndNs = b.EndNs
		s.bits = nil
	}
}

func (s *i2cState) finalize() {
	s.closeTx("")
	for i := range s.txs {
		if s.txs[i].ID == "" {
			s.txs[i].ID = fmt.Sprintf("i2c-tx-%d-%s", s.txs[i].StartNs, s.txs[i].Device)
		}
	}
}

// runI2C executes the state machine once. ov maps ambiguity keys to "use the
// alternate resolution", which is how candidate forks are replayed.
func runI2C(cap *model.Capture, p model.Params, ov map[string]bool) ([]model.Transaction, []ambiguity, []model.Diagnostic) {
	sclCh := findChannel(cap, "SCL")
	sdaCh := findChannel(cap, "SDA")
	if sclCh == nil || sdaCh == nil {
		return nil, nil, nil
	}
	scl, sda := buildLine(cap, "SCL"), buildLine(cap, "SDA")
	s := &i2cState{cap: cap, p: p, ov: ov, sclLvl: 1, sdaLvl: 1, sclFellT: -1, lastEdgeT: -1}
	if len(scl.edges) > 0 {
		s.sclLvl = 1 - scl.edges[0].level
	}
	if len(sda.edges) > 0 {
		s.sdaLvl = 1 - sda.edges[0].level
	}

	type ev struct {
		t     int64
		isSCL bool
		level int
		idx   int
	}
	var evs []ev
	for _, e := range scl.edges {
		evs = append(evs, ev{e.t, true, e.level, e.idx})
	}
	for _, e := range sda.edges {
		evs = append(evs, ev{e.t, false, e.level, e.idx})
	}
	// deterministic order: time, then SCL before SDA, then edge index
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0; j-- {
			a, b := evs[j-1], evs[j]
			less := b.t < a.t || (b.t == a.t && ((b.isSCL && !a.isSCL) || (b.isSCL == a.isSCL && b.idx < a.idx)))
			if !less {
				break
			}
			evs[j-1], evs[j] = evs[j], evs[j-1]
		}
	}

	for _, e := range evs {
		// data gap: terminate, never stitch across
		if s.tx != nil && s.lastEdgeT >= 0 && e.t-s.lastEdgeT > p.GapThresholdNs {
			s.diag = append(s.diag, model.Diagnostic{
				Code: "I2C_DATA_GAP", Severity: "warning", TimeNs: s.lastEdgeT, EdgeIndex: -1,
				Message: fmt.Sprintf("data gap of %d ns without STOP; transaction terminated, not stitched", e.t-s.lastEdgeT),
			})
			s.closeTx("gap_terminated")
		}
		s.lastEdgeT = e.t

		if e.isSCL {
			if e.level == 1 { // SCL rising: sample point
				s.onSclRise(e.t, e.idx)
			} else {
				s.sclFellT = e.t
			}
			s.sclLvl = e.level
		} else { // SDA edge
			s.onSdaEdge(e.t, e.level, e.idx)
			s.sdaLvl = e.level
		}
	}
	s.finalize()
	return s.txs, s.ambs, s.diag
}

func (s *i2cState) onSclRise(t int64, idx int) {
	if s.tx == nil {
		return
	}
	// clock stretching: SCL held low beyond threshold
	if s.sclFellT >= 0 && t-s.sclFellT > s.p.StretchThresholdNs {
		s.tx.Flags = append(s.tx.Flags, "clock_stretch")
		s.diag = append(s.diag, model.Diagnostic{
			Code: "I2C_CLOCK_STRETCH", Severity: "info", TimeNs: s.sclFellT, EdgeIndex: -1,
			Message: fmt.Sprintf("SCL held low for %d ns (clock stretching)", t-s.sclFellT),
		})
		if len(s.sdaSinceRise) > 0 {
			key := ambKey("clock_stretch", t)
			s.ambs = append(s.ambs, ambiguity{
				reason: "clock_stretch", timeNs: t, channel: "SCL",
				primary:   "stretch_only",
				alternate: "hidden_clock_edge",
				diagnostic: model.Diagnostic{
					Code: "I2C_STRETCH_AMBIGUOUS", Severity: "warning", TimeNs: t, EdgeIndex: idx,
					Message: "SDA moved during clock stretch; a hidden clock edge cannot be ruled out",
				},
			})
			if s.useAlt("clock_stretch", t) {
				mid := s.sdaSinceRise[0]
				s.pushBit(mid.level, mid.t, mid.idx)
				s.pushBit(s.sdaLvl, t, idx)
				s.sdaSinceRise = nil
				_ = key
				return
			}
		}
	}
	// missing clock edge: multiple SDA transitions inside one SCL-low cell
	if len(s.sdaSinceRise) >= 2 {
		s.ambs = append(s.ambs, ambiguity{
			reason: "missing_clock_edge", timeNs: t, channel: "SCL",
			primary:   "collapse_to_one_bit",
			alternate: "insert_missed_bit",
			diagnostic: model.Diagnostic{
				Code: "I2C_MISSING_CLOCK", Severity: "warning", TimeNs: t, EdgeIndex: idx,
				Message: fmt.Sprintf("%d SDA transitions within one SCL cell; a clock edge may be missing", len(s.sdaSinceRise)),
			},
		})
		if s.useAlt("missing_clock_edge", t) {
			mid := s.sdaSinceRise[0]
			s.pushBit(mid.level, mid.t, mid.idx)
			s.pushBit(s.sdaLvl, t, idx)
			s.sdaSinceRise = nil
			return
		}
	}
	// boundary sampling: SDA moved inside the boundary window before the rise
	if n := len(s.sdaSinceRise); n > 0 && t-s.sdaSinceRise[n-1].t <= s.p.BoundaryWindowNs {
		last := s.sdaSinceRise[n-1]
		s.ambs = append(s.ambs, ambiguity{
			reason: "boundary_sampling", timeNs: t, channel: "SDA",
			primary:   "sample_after_edge",
			alternate: "sample_before_edge",
			diagnostic: model.Diagnostic{
				Code: "I2C_BOUNDARY_SAMPLE", Severity: "warning", TimeNs: t, EdgeIndex: idx,
				Message: fmt.Sprintf("SDA edge %d ns before SCL rise is inside the boundary window", t-last.t),
			},
		})
		if s.useAlt("boundary_sampling", t) {
			s.pushBit(1-last.level, t, idx, last.idx)
			s.sdaSinceRise = nil
			return
		}
	}
	s.pushBit(s.sdaLvl, t, idx)
	s.sdaSinceRise = nil
}

func (s *i2cState) onSdaEdge(t int64, level, idx int) {
	if s.sclLvl == 1 {
		if level == 0 { // START or repeated START
			if s.tx != nil {
				s.ambs = append(s.ambs, ambiguity{
					reason: "repeated_start", timeNs: t, channel: "SDA",
					primary:   "same_transaction",
					alternate: "implicit_stop_new_transaction",
					diagnostic: model.Diagnostic{
						Code: "I2C_REPEATED_START", Severity: "info", TimeNs: t, EdgeIndex: idx,
						Message: "repeated START: same-transaction and implicit-STOP readings both kept",
					},
				})
				if s.useAlt("repeated_start", t) {
					s.closeTx("implicit_stop")
					s.openTx(t, false)
					return
				}
				s.closeTx("")
				s.openTx(t, true)
				return
			}
			s.openTx(t, false)
			return
		}
		// SDA rising while SCL high: STOP
		if s.tx != nil {
			s.closeTx("")
		}
		return
	}
	// SCL low: data transition
	s.sdaSinceRise = append(s.sdaSinceRise, edgeAt{t: t, level: level, idx: idx})
}

// decodeI2C runs the base decode plus one fork per ambiguity, keeping every
// legal candidate.
func decodeI2C(cap *model.Capture, p model.Params) model.DecodeResult {
	base, ambs, diags := runI2C(cap, p, nil)
	res := model.DecodeResult{Diagnostics: diags}
	if len(base) > 0 {
		res.Candidates = append(res.Candidates, makeCandidate("i2c", "base", confidence(len(ambs), true), base, diags))
	}
	for _, a := range ambs {
		ov := map[string]bool{ambKey(a.reason, a.timeNs): true}
		txs, _, _ := runI2C(cap, p, ov)
		if len(txs) == 0 {
			continue
		}
		hyp := fmt.Sprintf("%s=%s", a.reason, a.alternate)
		res.Candidates = append(res.Candidates, makeCandidate("i2c", hyp, confidence(len(ambs), false), txs, append(append([]model.Diagnostic(nil), diags...), a.diagnostic)))
	}
	return res
}

func confidence(nAmb int, base bool) float64 {
	if nAmb == 0 {
		return 0.98
	}
	if base {
		return 0.55
	}
	return 0.45
}

func makeCandidate(protocol, hypothesis string, conf float64, txs []model.Transaction, diags []model.Diagnostic) model.Candidate {
	reason := "unique_decode"
	if hypothesis != "base" {
		reason = hypothesis
		for i := 0; i < len(reason); i++ {
			if reason[i] == '=' {
				reason = reason[:i]
				break
			}
		}
	}
	id := protocol + "-c-" + hypothesis
	for _, tx := range txs {
		id += "-" + tx.ID
	}
	if len(id) > 96 {
		id = id[:96]
	}
	return model.Candidate{
		ID: id, Protocol: protocol, Reason: reason, Hypothesis: hypothesis,
		Confidence: conf, Transactions: txs, Diagnostics: diags,
	}
}

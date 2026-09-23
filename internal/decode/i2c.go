package decode

import "fmt"

// I2C structured findings (machine-readable evidence):
//   i2c.start               info    START condition
//   i2c.repeated_start      info    START while bus already active at a frame boundary
//   i2c.stop                info    STOP condition
//   i2c.clock_stretch       warning SCL held low by a slave beyond threshold
//   i2c.missing_clock_edge  error   unpaired SCL edge / frame not multiple of 9
//   i2c.data_gap            error   idle gap ends an incomplete transaction
//   i2c.boundary_sample     warning SDA moved during SCL high mid-byte
//   i2c.polarity_ambiguous  info    clock polarity unknown; both candidates kept
//   i2c.nack                info    slave NACK on an address/data slot

type i2cPulse struct {
	rising    levelEvent
	falling   levelEvent
	sdaAtRise int
	sdaAtFall int
	hasRise   bool
	hasFall   bool
}

type i2cSegment struct {
	startEdge levelEvent
	stopEdge  *levelEvent
	pulses    []i2cPulse
	stretchNS int64
	end       int64
}

type i2cTxn struct {
	segments   []i2cSegment
	start, end int64
	incomplete bool
	stopSeen   bool
}

type i2cScanner struct {
	st        *edgeStream
	txn       *i2cTxn
	seg       *i2cSegment
	pulse     *i2cPulse
	prevFallT int64
	findings  []Finding
	rawTxns   []i2cTxn
}

func decodeI2C(st *edgeStream, res *DecodeResult) error {
	if st.roleCh["scl"] == nil || st.roleCh["sda"] == nil {
		return &SemanticError{Diagnostics: []Diagnostic{{
			Code: "missing_role_channel", Path: "channels",
			Message: "i2c capture requires channels with roles scl and sda", Severity: "error",
		}}}
	}
	gap := st.cap.Params.GapNS
	if gap <= 0 {
		gap = 100_000
	}
	stretch := st.cap.Params.StretchNS
	if stretch <= 0 {
		stretch = 5_000
	}
	sc := &i2cScanner{st: st}
	lastAct := int64(0)
	for i := range st.evs {
		ev := &st.evs[i]
		if !ev.valid || (ev.role != "scl" && ev.role != "sda") {
			continue
		}
		if sc.txn != nil && lastAct > 0 && ev.t-lastAct > gap {
			sc.addFinding("i2c.data_gap", "error", lastAct, ev.t, "scl",
				fmt.Sprintf("no bus activity for %d ns (> gap %d ns); transaction is not stitched", ev.t-lastAct, gap),
				"", "")
			sc.txn.incomplete = true
			sc.txn.end = lastAct
			sc.emitTxn()
		}
		if ev.role == "sda" {
			sc.onSDA(ev)
		} else {
			sc.onSCL(ev, stretch)
		}
		lastAct = ev.t
	}
	sc.flush()
	res.Transactions = sc.buildTransactions()
	res.Findings = append(res.Findings, sc.findings...)
	return nil
}

func (sc *i2cScanner) onSDA(ev *levelEvent) {
	if sc.st.levelAt("scl", ev.t) != 1 {
		return // normal setup/hold change while SCL is low
	}
	atBoundary := sc.seg == nil || len(sc.seg.pulses)%9 == 0
	switch {
	case ev.logic == 0 && (sc.txn == nil || sc.seg == nil):
		sc.openTxn(ev)
		sc.addFinding("i2c.start", "info", ev.t, ev.t, "sda", "START condition", ev.edge.ID, "")
	case ev.logic == 0 && atBoundary:
		sc.repeatedStart(ev)
	case ev.logic == 0:
		sc.addFinding("i2c.boundary_sample", "warning", ev.t, ev.t, "sda",
			"SDA fell while SCL high mid-byte; not a legal repeated START", ev.edge.ID, "")
		if sc.pulse != nil {
			sc.pulse.sdaAtRise = 0
		}
	case ev.logic == 1 && sc.txn != nil && atBoundary:
		sc.stop(ev)
	case ev.logic == 1 && sc.txn != nil:
		sc.addFinding("i2c.boundary_sample", "warning", ev.t, ev.t, "sda",
			"SDA rose while SCL high mid-byte; not a legal STOP", ev.edge.ID, "")
	}
}

func (sc *i2cScanner) openTxn(ev *levelEvent) {
	sc.txn = &i2cTxn{start: ev.t, end: ev.t}
	seg := i2cSegment{startEdge: *ev, end: ev.t}
	sc.txn.segments = append(sc.txn.segments, seg)
	sc.seg = &sc.txn.segments[0]
	sc.pulse = nil
}

func (sc *i2cScanner) repeatedStart(ev *levelEvent) {
	if sc.pulse != nil { // dangling partial frame
		sc.addFinding("i2c.missing_clock_edge", "error", sc.pulse.rising.t, ev.t, "scl",
			"repeated START arrived before the frame completed", ev.edge.ID, "")
		sc.pulse = nil
	}
	seg := i2cSegment{startEdge: *ev, end: ev.t}
	sc.txn.segments = append(sc.txn.segments, seg)
	sc.seg = &sc.txn.segments[len(sc.txn.segments)-1]
	sc.txn.end = ev.t
	sc.addFinding("i2c.repeated_start", "info", ev.t, ev.t, "sda",
		"repeated START at frame boundary; the transaction continues without stitching",
		ev.edge.ID, "")
}

func (sc *i2cScanner) stop(ev *levelEvent) {
	stop := *ev
	sc.seg.stopEdge = &stop
	sc.seg.end = ev.t
	sc.txn.stopSeen = true
	sc.txn.end = ev.t
	sc.addFinding("i2c.stop", "info", ev.t, ev.t, "sda", "STOP condition", ev.edge.ID, "")
	sc.emitTxn()
}

func (sc *i2cScanner) onSCL(ev *levelEvent, stretch int64) {
	if sc.txn == nil {
		return
	}
	if ev.logic == 1 {
		if sc.pulse != nil {
			sc.addFinding("i2c.missing_clock_edge", "error", sc.pulse.rising.t, ev.t, "scl",
				"two SCL rising edges with no falling edge between them", ev.edge.ID, "")
			sc.txn.incomplete = true
		}
		if sc.prevFallT > 0 && ev.t-sc.prevFallT > stretch {
			d := ev.t - sc.prevFallT
			sc.seg.stretchNS += d
			sc.addFinding("i2c.clock_stretch", "warning", sc.prevFallT, ev.t, "scl",
				fmt.Sprintf("SCL held low for %d ns (> stretch threshold %d ns)", d, stretch),
				ev.edge.ID, "")
		}
		sc.pulse = &i2cPulse{rising: *ev, hasRise: true,
			sdaAtRise: sc.st.levelAt("sda", ev.t), sdaAtFall: sc.st.levelAt("sda", ev.t)}
	} else {
		if sc.pulse == nil {
			sc.addFinding("i2c.missing_clock_edge", "error", ev.t, ev.t, "scl",
				"SCL falling edge without a preceding rising edge", ev.edge.ID, "")
			sc.txn.incomplete = true
			return
		}
		sc.pulse.falling = *ev
		sc.pulse.hasFall = true
		sc.pulse.sdaAtFall = sc.st.levelAt("sda", ev.t)
		sc.seg.pulses = append(sc.seg.pulses, *sc.pulse)
		sc.seg.end = ev.t
		sc.txn.end = ev.t
		sc.prevFallT = ev.t
		sc.pulse = nil
	}
}

func (sc *i2cScanner) flush() {
	if sc.txn == nil {
		return
	}
	if sc.pulse != nil {
		sc.addFinding("i2c.missing_clock_edge", "error", sc.pulse.rising.t, sc.pulse.rising.t, "scl",
			"rising SCL edge with no matching falling edge before capture end",
			sc.pulse.rising.edge.ID, "")
		sc.txn.incomplete = true
		sc.txn.end = sc.pulse.rising.t
	}
	for si := range sc.txn.segments {
		seg := &sc.txn.segments[si]
		if seg.stopEdge == nil && len(seg.pulses)%9 != 0 {
			sc.addFinding("i2c.missing_clock_edge", "error", seg.startEdge.t, seg.end, "scl",
				fmt.Sprintf("segment ends with %d SCL pulses, not a multiple of 9", len(seg.pulses)),
				seg.startEdge.edge.ID, "")
			sc.txn.incomplete = true
		}
	}
	if !sc.txn.stopSeen {
		sc.txn.incomplete = true
	}
	sc.emitTxn()
}

func (sc *i2cScanner) emitTxn() {
	if sc.txn == nil {
		return
	}
	sc.buildCandidates(sc.txn)
	sc.rawTxns = append(sc.rawTxns, *sc.txn)
	sc.txn = nil
	sc.seg = nil
	sc.pulse = nil
	sc.prevFallT = 0
}

func (sc *i2cScanner) addFinding(code, sev string, s, e int64, ch, detail, edgeID, candID string) {
	sc.findings = append(sc.findings, Finding{
		ID:   hashNS("fd_", sc.st.cap.ID, code, ch, itoa(int(s)), itoa(int(e)), detail),
		Code: code, Kind: "state", Severity: sev, StartNS: s, EndNS: e, Channel: ch,
		Detail: detail, RefEdge: edgeID, RefCand: candID,
	})
}

func (sc *i2cScanner) buildCandidates(t *i2cTxn) {
	// nothing mutable here; candidates are derived in buildTransactions from
	// the stored pulse structure for both sampling-edge polarities.
	_ = t
}

// frame describes one assembled 8-bit + ACK slot per candidate polarity.
type i2cFrame struct {
	value      int
	ack        string
	valid      bool
	invalid    string
	startNS    int64
	endNS      int64
	clockEdges []string
	frameEdges []string
	ackEdges   []string
	boundary   bool
	address    bool
}

func (sc *i2cScanner) buildTransactions() []Transaction {
	var out []Transaction
	capID := sc.st.cap.ID
	for ti := range sc.rawTxns {
		raw := &sc.rawTxns[ti]
		polarities := []string{"rising"}
		if sc.st.cap.Params.ClockPolarity == "unknown" || sc.st.cap.Params.ClockPolarity == "" {
			polarities = []string{"rising", "falling"}
		}
		edgeSet := []string{}
		for si := range raw.segments {
			seg := &raw.segments[si]
			edgeSet = append(edgeSet, seg.startEdge.edge.ID)
			for _, p := range seg.pulses {
				if p.hasRise {
					edgeSet = append(edgeSet, p.rising.edge.ID)
				}
				if p.hasFall {
					edgeSet = append(edgeSet, p.falling.edge.ID)
				}
			}
			if seg.stopEdge != nil {
				edgeSet = append(edgeSet, seg.stopEdge.edge.ID)
			}
		}
		edgeSet = uniqEdgeIDs(edgeSet)

		var cands []Candidate
		for ci, pol := range polarities {
			var bytes []Byte
			byteIdx := 0
			for si := range raw.segments {
				seg := &raw.segments[si]
				fs := sc.assembleSegment(seg, pol)
				for _, f := range fs {
					idSrc := fmt.Sprintf("%s|i2c|%d|%d|%s|%02x", capID, ti, byteIdx, pol, f.value)
					b := Byte{
						ID: hashNS("by_", idSrc), Index: byteIdx, Value: f.value,
						Hex: fmt.Sprintf("%02X", f.value), StartNS: f.startNS, EndNS: f.endNS,
						ClockEdges: f.clockEdges, FrameEdges: f.frameEdges, Ack: f.ack,
						AckEdges: f.ackEdges, Valid: f.valid, InvalidCode: f.invalid,
						Kind: "data",
					}
					if f.address {
						b.Kind = "address"
					}
					bytes = append(bytes, b)
					byteIdx++
				}
			}
			conf := 1.0
			mode := "sample@rising"
			note := ""
			if pol == "falling" {
				mode = "sample@falling"
			}
			if len(polarities) == 2 {
				conf = 0.5
				note = "clock polarity unknown: rising/falling sampling both retained"
				sc.addFindingAtBuild("i2c.polarity_ambiguous", "info", raw.start, raw.end, "scl", note)
			}
			cid := hashNS("cd_", capID, "i2c", itoa(ti), mode, sc.st.cap.DecoderVer)
			cands = append(cands, Candidate{
				ID: cid, Index: ci, DecoderVer: sc.st.cap.DecoderVer, Mode: mode,
				Confidence: conf, Bytes: bytes, Note: note, EdgeIDs: edgeSet,
			})
		}

		device := ""
		if len(cands) > 0 && len(cands[0].Bytes) > 0 {
			device = fmt.Sprintf("0x%02X", cands[0].Bytes[0].Value>>1)
		}
		status := "complete"
		if raw.incomplete || !raw.stopSeen {
			status = "incomplete"
		}
		tid := hashNS("tx_", capID, "i2c", itoa(ti), itoa(int(raw.start)), itoa(int(raw.end)))
		out = append(out, Transaction{
			ID: tid, Index: ti, Protocol: "i2c", Device: device,
			StartNS: raw.start, EndNS: raw.end, Status: status,
			Segments: len(raw.segments), Candidate: cands, EdgeIDs: edgeSet,
		})
	}
	return out
}

func (sc *i2cScanner) addFindingAtBuild(code, sev string, s, e int64, ch, detail string) {
	id := hashNS("fd_", sc.st.cap.ID, code, ch, itoa(int(s)), itoa(int(e)), detail)
	for _, f := range sc.findings {
		if f.ID == id {
			return
		}
	}
	sc.findings = append(sc.findings, Finding{
		ID: id, Code: code, Kind: "state", Severity: sev, StartNS: s, EndNS: e,
		Channel: ch, Detail: detail,
	})
}

func (sc *i2cScanner) assembleSegment(seg *i2cSegment, polarity string) []i2cFrame {
	var frames []i2cFrame
	msbFirst := sc.st.cap.Params.BitOrder != "lsb"
	for off := 0; off+8 <= len(seg.pulses); off += 9 {
		var val, ackBit int
		clockEdges := []string{}
		frameEdges := []string{}
		boundary := false
		for bit := 0; bit < 8; bit++ {
			p := seg.pulses[off+bit]
			sample := p.sdaAtRise
			if polarity == "falling" {
				sample = p.sdaAtFall
			}
			if sample == 1 {
				if msbFirst {
					val |= 1 << uint(7-bit)
				} else {
					val |= 1 << uint(bit)
				}
			}
			if p.hasRise {
				clockEdges = append(clockEdges, p.rising.edge.ID)
			}
			if p.hasFall {
				clockEdges = append(clockEdges, p.falling.edge.ID)
			}
			frameEdges = append(frameEdges, seg.startEdge.edge.ID)
		}
		f := i2cFrame{value: val, address: off == 0, clockEdges: clockEdges}
		first := seg.pulses[off]
		last := seg.pulses[off+7]
		if first.hasRise {
			f.startNS = first.rising.t
		}
		if last.hasFall {
			f.endNS = last.falling.t
		}
		if off+9 <= len(seg.pulses) {
			ap := seg.pulses[off+8]
			ackBit = ap.sdaAtRise
			if polarity == "falling" {
				ackBit = ap.sdaAtFall
			}
			if ackBit == 0 {
				f.ack = "ACK"
			} else {
				f.ack = "NACK"
			}
			if ap.hasRise {
				f.ackEdges = append(f.ackEdges, ap.rising.edge.ID)
			}
			if ap.hasFall {
				f.ackEdges = append(f.ackEdges, ap.falling.edge.ID)
			}
			if f.endNS == 0 || ap.falling.t > f.endNS {
				f.endNS = ap.falling.t
			}
		} else {
			f.valid = false
			f.invalid = "missing_clock_edge"
		}
		if f.ack == "NACK" && f.valid {
			sc.addFindingAtBuild("i2c.nack", "info", f.startNS, f.endNS, "sda",
				fmt.Sprintf("NACK on %s byte 0x%02X", map[bool]string{true: "address", false: "data"}[f.address], val))
		}
		for _, bcode := range sc.findingsInRange("i2c.boundary_sample", f.startNS, f.endNS) {
			if bcode {
				boundary = true
			}
		}
		if boundary {
			f.valid = false
			f.invalid = "boundary_sample"
			f.boundary = true
		}
		frames = append(frames, f)
	}
	if rem := len(seg.pulses) % 9; rem != 0 {
		off := len(seg.pulses) - rem
		var val int
		clockEdges := []string{}
		for bit := 0; bit < rem; bit++ {
			p := seg.pulses[off+bit]
			sample := p.sdaAtRise
			if polarity == "falling" {
				sample = p.sdaAtFall
			}
			if sample == 1 {
				val |= 1 << uint(rem-1-bit)
			}
			if p.hasRise {
				clockEdges = append(clockEdges, p.rising.edge.ID)
			}
			if p.hasFall {
				clockEdges = append(clockEdges, p.falling.edge.ID)
			}
		}
		frames = append(frames, i2cFrame{
			value: val, valid: false, invalid: "missing_clock_edge",
			startNS: seg.pulses[off].rising.t, endNS: seg.end, clockEdges: clockEdges,
			address: len(seg.pulses) <= 9,
		})
	}
	return frames
}

func (sc *i2cScanner) findingsInRange(code string, from, to int64) []bool {
	var hit []bool
	for _, f := range sc.findings {
		if f.Code == code && f.StartNS >= from && f.StartNS <= to {
			hit = append(hit, true)
		}
	}
	if len(hit) == 0 {
		hit = append(hit, false)
	}
	return hit
}

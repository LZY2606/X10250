package decode

import (
	"fmt"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

type marker struct {
	t    float64
	kind string // start|rstart|stop
	sda  physEdge
	scl  bool
}

type bit struct {
	value    int
	at       float64
	boundary bool
	edgeRefs []string
	sclRise  physEdge
}

type frameData struct {
	bits       []bit
	ack        string
	kind       string
	startT     float64
	endT       float64
	hasStretch bool
	stretchAt  float64
	missing    bool
	partial    int
	edgeRefs   []string
}

type i2cGroup struct {
	frames  []frameData
	variant string
	note    string
	invalid string
}

func (e *engine) sclSDA() (string, string, bool) {
	scl := e.aliasesForRole("scl")
	sda := e.aliasesForRole("sda")
	if len(scl) == 0 || len(sda) == 0 {
		return "", "", false
	}
	return scl[0], sda[0], true
}

// markersFor builds the ordered START / repeated-START / STOP marker list.
func (e *engine) markersFor(scl, sda string, invertClock bool) []marker {
	var ms []marker
	for _, ed := range sdaEdges(e.tl.byCh[sda]) {
		l, _ := e.tl.levelAt(scl, ed.t)
		if invertClock {
			l = !l
		}
		if !l {
			continue
		}
		kind := "start"
		if ed.level {
			kind = "stop"
		}
		if len(ms) > 0 && ms[len(ms)-1].kind != "stop" {
			if ed.level {
				kind = "stop"
			} else {
				kind = "rstart"
			}
		}
		if kind == "start" && len(ms) > 0 && ms[len(ms)-1].kind == "stop" {
			// new transaction after stop
		}
		ms = append(ms, marker{t: ed.t, kind: kind, sda: ed, scl: l})
	}
	return ms
}

func sdaEdges(es []physEdge) []physEdge { return es }

// sclEdges returns effective clock edges, optionally polarity-flipped.
func effectiveClk(es []physEdge, invert bool) []physEdge {
	if !invert {
		return es
	}
	out := make([]physEdge, len(es))
	for i, x := range es {
		x.level = !x.level
		out[i] = x
	}
	return out
}

func (e *engine) judge(rule, outcome, detail string, at float64, refs []string) {
	e.judgments = append(e.judgments, model.Judgment{
		Rule: rule, Outcome: outcome, Detail: detail,
		AtNs: ns(at), EdgeRefs: refs,
	})
}

func (e *engine) decodeI2C() *ingest.Error {
	scl, sda, ok := e.sclSDA()
	if !ok {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve, Message: "i2c decode requires scl and sda probe roles"}
	}
	pol := e.tl.probe[scl].ClkPolRaw
	invertClock := pol == "1" || pol == "high"
	e.judge("CLOCK_POLARITY_APPLIED", "i2c",
		fmt.Sprintf("scl clock polarity raw=%q invert=%d", pol, b2i(invertClock)), 0, nil)

	ms := e.markersFor(scl, sda, invertClock)
	eff := effectiveClk(e.tl.byCh[scl], invertClock)
	rises0 := edgesOfKind(eff, true)
	falls0 := edgesOfKind(eff, false)

	for _, m := range ms {
		rule, out := "START_DETECTED", "transaction_open"
		if m.kind == "rstart" {
			rule, out = "REPEATED_START_DETECTED", "new_frame_same_transaction"
		}
		if m.kind == "stop" {
			rule, out = "STOP_DETECTED", "transaction_close"
		}
		e.judge(rule, out, fmt.Sprintf("sda edge at %.1f ns while scl high", ns(m.t)), ns(m.t), []string{m.sda.id})
	}
	if len(ms) == 0 {
		return &ingest.Error{Code: ingest.ErrSemanticNoSolve,
			Message: "i2c: no START/STOP marker found under given clock polarity"}
	}

	// Delimit frames between consecutive markers.
	type span struct{ start, end marker }
	var spans []span
	for i := 0; i+1 < len(ms); i++ {
		if ms[i].kind == "stop" {
			continue
		}
		spans = append(spans, span{ms[i], ms[i+1]})
	}

	var frames []frameData
	for fi, sp := range spans {
		fd := frameData{startT: sp.start.t, endT: sp.end.t}
		for _, r := range rises0 {
			if r.t <= sp.start.t || r.t >= sp.end.t {
				continue
			}
			lvl, defID := e.tl.levelAt(sda, r.t)
			refs := []string{r.id}
			if defID != "" {
				refs = append(refs, defID)
			}
			bnd := len(e.tl.edgesNear(sda, r.t, boundaryJitter)) > 0
			fd.bits = append(fd.bits, bit{value: b2i(lvl), at: r.t, boundary: bnd, edgeRefs: refs, sclRise: r})
		}
		if len(fd.bits) == 0 {
			continue
		}
		// Clock stretching: low phase longer than threshold.
		for _, r := range rises0 {
			if r.t <= sp.start.t || r.t >= sp.end.t {
				continue
			}
			for _, f := range falls0 {
				if f.t > r.t {
					var nextRise *physEdge
					for k := range rises0 {
						if rises0[k].t > f.t && rises0[k].t < sp.end.t {
							nextRise = &rises0[k]
							break
						}
					}
					if nextRise != nil && nextRise.t-f.t > e.params.I2CStretchNs*1e-9 {
						fd.hasStretch = true
						fd.stretchAt = f.t
						fd.edgeRefs = append(fd.edgeRefs, f.id, nextRise.id)
						e.judge("CLOCK_STRETCH_DETECTED", "keep_transaction",
							fmt.Sprintf("scl low for %.0f ns > threshold %.0f ns", ns(nextRise.t-f.t), e.params.I2CStretchNs),
							ns(f.t), []string{f.id, nextRise.id})
					}
					break
				}
			}
		}
		total := len(fd.bits)
		if rem := total % 9; rem != 0 {
			fd.missing = true
			fd.partial = rem
			last := fd.bits[len(fd.bits)-1]
			e.judge("MISSING_CLOCK_EDGE", "keep_multiple_candidates",
				fmt.Sprintf("frame %d ends with %d/9 bits; truncated and partial candidates kept", fi, rem),
				last.at, last.edgeRefs)
		}
		frames = append(frames, fd)
	}

	// Cut transactions at STOP and at data gaps; never stitch across a gap.
	gapTh := e.params.GapNs * 1e-9
	type txn struct {
		frames            []int
		gapLeft, gapRight bool
	}
	var txns []txn
	cur := txn{}
	for fi, fd := range frames {
		if len(cur.frames) > 0 {
			prev := frames[cur.frames[len(cur.frames)-1]]
			if fd.startT-prev.endT > gapTh {
				gapAt := fd.startT
				refs := []string{msRef(ms, fd.startT)}
				e.judge("DATA_GAP", "split_do_not_stitch",
					fmt.Sprintf("%.0f ns idle > gap threshold %.0f ns", ns(fd.startT-prev.endT), e.params.GapNs),
					ns(gapAt), refs)
				txns = append(txns, cur)
				cur = txn{gapLeft: true}
			}
		}
		cur.frames = append(cur.frames, fi)
		// stop after this frame?
		if fi+1 < len(ms) {
			_ = fi
		}
	}
	if len(cur.frames) > 0 {
		txns = append(txns, cur)
	}

	if !e.reserve(len(txns)*2 + 1) {
		return &ingest.Error{Code: ingest.ErrBudgetExhausted, Message: "i2c candidate budget exhausted"}
	}
	for ti, tx := range txns {
		e.emitI2CTxn(scl, sda, ti, tx, frames, false)
		if tx.gapLeft || tx.gapRight {
			e.emitI2CTxn(scl, sda, ti, tx, frames, true)
		}
	}
	return nil
}

func msRef(ms []marker, t float64) string {
	for _, m := range ms {
		if m.t == t {
			return m.sda.id
		}
	}
	return ""
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (e *engine) emitI2CTxn(scl, sda string, idx int, tx struct {
	frames            []int
	gapLeft, gapRight bool
}, frames []frameData, stitched bool) {
	boundaryFrames := map[int]bool{}
	hasBoundary := false
	for _, fi := range tx.frames {
		for _, b := range frames[fi].bits {
			if b.boundary {
				hasBoundary = true
			}
		}
	}
	emit := func(flipBoundary bool) {
		c := model.Candidate{Bus: "i2c", Mode: "i2c", FrameIdx: idx}
		var allRefs []string
		startT, endT := 0.0, 0.0
		for k, fi := range tx.frames {
			fd := frames[fi]
			if k == 0 {
				startT = fd.startT
			}
			endT = fd.endT
			c.Evidence = append(c.Evidence, model.Evidence{
				Kind: "start", AtNs: ns(fd.startT),
				Detail: "START opens frame",
			})
			val := 0
			known := true
			for bi := 0; bi < 8 && bi < len(fd.bits); bi++ {
				b := fd.bits[bi]
				v := b.value
				if b.boundary && flipBoundary {
					v = 1 - v
					c.Evidence = append(c.Evidence, model.Evidence{
						Kind: "boundary_sample", AtNs: ns(b.at),
						Detail: fmt.Sprintf("sda edge within %.1f ns of scl sample edge; alternative bit kept", boundaryJitter*1e9),
						Edges:  b.edgeRefs,
					})
				}
				val = (val << 1) | v
				allRefs = append(allRefs, b.edgeRefs...)
			}
			if len(fd.bits) < 8 {
				known = false
			}
			ack := "unknown"
			if len(fd.bits) >= 9 {
				switch fd.bits[8].value {
				case 0:
					ack = "ack"
				default:
					ack = "nack"
				}
				allRefs = append(allRefs, fd.bits[8].edgeRefs...)
				allRefs = append(allRefs, fd.bits[8].sclRise.id)
			}
			kind := "data"
			if k == 0 {
				kind = "addr"
			}
			hexV := fmt.Sprintf("%02X", val)
			by := model.Byte{
				Index: k, Value: nil, ValueHex: hexV, Unknown: !known,
				Kind: kind, Ack: ack,
				Valid: model.Interval{StartNs: ns(fd.startT), EndNs: ns(fd.endT), Basis: "normalized-from-raw-ns"},
			}
			if known {
				by.Value = &val
			}
			if fd.missing && len(fd.bits)%9 != 0 {
				by.Kind = "partial"
			}
			for _, b := range fd.bits {
				by.EdgeRefs = append(by.EdgeRefs, b.edgeRefs...)
			}
			c.Bytes = append(c.Bytes, by)
			if fd.hasStretch {
				c.Evidence = append(c.Evidence, model.Evidence{
					Kind: "stretch", AtNs: ns(fd.stretchAt), Detail: "slave held scl low; transaction kept",
					Edges: fd.edgeRefs,
				})
			}
			if fd.missing {
				last := fd.bits[len(fd.bits)-1]
				c.Evidence = append(c.Evidence, model.Evidence{
					Kind: "missing_edge", AtNs: ns(last.at),
					Detail: fmt.Sprintf("%d/9 clock edges observed; byte value not unique", len(fd.bits)%9),
					Edges:  last.edgeRefs,
				})
			}
			boundaryFrames[fi] = true
		}
		c.Valid = model.Interval{StartNs: ns(startT), EndNs: ns(endT), Basis: "normalized-from-raw-ns"}
		c.GroupID = fmt.Sprintf("G-i2c-%d-%s", idx, ingest.Hash8(fmt.Sprintf("%.1f", startT)))
		conf := 0.9
		c.Conf = conf
		if stitched {
			c.Status = "invalid"
			c.InvalidReason = "data_gap_must_not_be_stitched"
			c.Decision = "rejected"
			c.RejectReason = "gap stitching violates DATA_GAP rule"
			conf = 0.05
			c.Evidence = append(c.Evidence, model.Evidence{Kind: "gap", AtNs: ns(startT),
				Detail: "candidate illegally joins frames across a data gap"})
		} else if hasBoundary {
			c.Status = "ambiguous"
			conf = 0.55
			c.Decision = "pending"
		} else {
			c.Status = "ok"
			c.Decision = "pending"
		}
		if !stitched {
			e.judge("TRANSACTION_KEEP", "candidate_emitted",
				fmt.Sprintf("i2c txn %d variant flip=%d bytes=%d", idx, b2i(flipBoundary), len(c.Bytes)),
				ns(startT), dedup(allRefs))
		}
		_ = scl
		_ = sda
		e.addCand(c)
	}
	emit(false)
	if hasBoundary {
		emit(true)
	}
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range in {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

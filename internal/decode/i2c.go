package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

type clkPol int

const (
	clkIdleLow clkPol = iota
	clkIdleHigh
)

type mergedEv struct {
	at   float64
	seq  int
	line string // scl|sda
	t    trans
}

func mergeEvents(tl map[string]*timeline) ([]mergedEv, bool, bool) {
	scl, ok1 := tl["scl"]
	sda, ok2 := tl["sda"]
	if !ok1 || !ok2 {
		return nil, ok1, ok2
	}
	var ev []mergedEv
	for _, x := range scl.tr {
		ev = append(ev, mergedEv{at: x.at, seq: x.seq, line: "scl", t: x})
	}
	for _, x := range sda.tr {
		ev = append(ev, mergedEv{at: x.at, seq: x.seq, line: "sda", t: x})
	}
	sort.SliceStable(ev, func(i, j int) bool {
		if ev[i].at != ev[j].at {
			return ev[i].at < ev[j].at
		}
		if ev[i].line != ev[j].line {
			return ev[i].line < ev[j].line
		}
		return ev[i].seq < ev[j].seq
	})
	return ev, true, true
}

func polarityOf(p model.Params) clkPol {
	if p.ClockPol == "idle-high" {
		return clkIdleHigh
	}
	return clkIdleLow
}

// scanI2C reassembles I2C transactions while preserving every ambiguity.
func scanI2C(tl map[string]*timeline, p model.Params) scannerOut {
	ev, hasSCL, hasSDA := mergeEvents(tl)
	out := scannerOut{}
	if !hasSCL || !hasSDA {
		out.noClockLine = true
		missing := "scl"
		if !hasSDA {
			missing = "sda"
		}
		out.diags = append(out.diags, model.Diagnostic{
			Code: "MISSING_SIGNAL_LINE", Severity: "error", Channel: missing,
			Message: fmt.Sprintf("I2C decode requires both scl and sda; %s is absent", missing),
		})
		return out
	}
	scl, sda := tl["scl"], tl["sda"]
	pol := polarityOf(p)

	stretchTol := defaultOr(p.StretchTol, 50e-6)
	gapTol := defaultOr(p.GapTol, 200e-6)
	boundaryTol := defaultOr(p.BoundaryTol, 20e-9)

	// trackers
	active := false
	var cur *rawFrame
	var samples []*bit // bits collected in the current byte (0..7 + ack)
	var sclDownAt float64
	lastActivity := -1.0
	frameSeq := 0

	startFrame := func(at float64, seqs []int, repeated bool, note string) {
		frameSeq++
		cur = &rawFrame{
			protocol:  "i2c",
			device:    "i2c-bus",
			startAt:   at,
			startSeqs: seqs,
			note:      note,
		}
		if repeated {
			cur.basis = append(cur.basis, model.BasisEvent{
				Code: model.BasisRepeatedStart, At: at, EdgeSeqs: seqs,
				Message: "repeated START: new transaction begins without STOP",
			})
		}
		samples = nil
	}
	closeByte := func() {
		if cur == nil || len(samples) == 0 {
			return
		}
		rb := rawByte{pos: len(cur.bytes), startSec: samples[0].sampleAt}
		for i, b := range samples {
			if i < 8 {
				rb.dataBits = append(rb.dataBits, b)
			} else {
				rb.ack = b
			}
		}
		rb.endSec = samples[len(samples)-1].sampleAt
		for _, b := range samples {
			rb.edgeSeqs = append(rb.edgeSeqs, b.edgeSeq)
			rb.sampleAts = append(rb.sampleAts, b.sampleAt)
		}
		cur.bytes = append(cur.bytes, rb)
		samples = nil
	}
	finishFrame := func(at float64, term string) {
		closeByte()
		if cur != nil {
			cur.endAt = at
			cur.terminated = term
			if len(cur.bytes) == 0 {
				cur.note = "frame contains no complete bytes"
			}
			if term == "data_gap" {
				cur.basis = append(cur.basis, model.BasisEvent{
					Code: model.BasisDataGap, At: at,
					Message: fmt.Sprintf("no bus activity for %.3fs; not joined to the next transaction", gapTol),
				})
			}
			out.frames = append(out.frames, *cur)
		}
		cur = nil
		active = false
		samples = nil
	}

	// idle and sampling edges depend on configured polarity
	startFalling := pol == clkIdleLow // SDA falls while SCL high
	idleLine := true                  // SCL idle level
	if pol == clkIdleHigh {
		idleLine = false
	}
	sampleOnRise := pol == clkIdleLow

	for _, e := range ev {
		if lastActivity >= 0 && active && e.at-lastActivity > gapTol {
			seq := e.seq
			out.diags = append(out.diags, model.Diagnostic{
				Code: "DATA_GAP", Severity: "warning", EdgeSeq: &seq,
				Channel: e.line, At: lastActivity,
				Message: fmt.Sprintf("data gap of %.3fs splits transactions; fragments are not stitched", e.at-lastActivity),
			})
			finishFrame(lastActivity, "data_gap")
		}

		switch e.line {
		case "scl":
			lvl, known := e.t.level, e.t.level != nil
			_ = known
			// clock stretching: SCL held at non-idle level far longer than tol
			if lvl != nil && *lvl != idleLine {
				if lastActivity >= 0 && sampleOnRise == (*lvl == false) && e.at-lastActivity > stretchTol && cur != nil {
					cur.basis = append(cur.basis, model.BasisEvent{
						Code: model.BasisClockStretch, At: sclDownAt, EdgeSeqs: []int{e.seq},
						Message: fmt.Sprintf("clock held low for %.3fus (stretch tolerance %.3fus)",
							(e.at-sclDownAt)*1e6, stretchTol*1e6),
					})
				}
			}
			rise := lvl != nil && *lvl == idleLine
			if active {
				if rise == sampleOnRise {
					// data bit sampling edge
					if len(samples) < 9 {
						val, known := sda.levelAt(e.at)
						bt := &bit{order: len(samples), edgeSeq: e.seq, sampleAt: e.at}
						if !known {
							bt.val = nil
							bt.basis = &model.BasisEvent{Code: model.BasisMissingClockEdge, At: e.at,
								EdgeSeqs: []int{e.seq}, Message: "SDA level unknown at clock edge; both bit values retained"}
						} else {
							if near, _, ok := sda.nearestTrans(e.at, boundaryTol); ok && near.at != e.at {
								bt.val = nil
								bt.basis = &model.BasisEvent{Code: model.BasisBoundarySample, At: e.at,
									EdgeSeqs: []int{e.seq, near.seq},
									Message: "SDA transition within boundary tolerance of sampling edge; both readings retained"}
							} else {
								bt.val = &val
							}
						}
						samples = append(samples, bt)
						if len(samples) == 9 {
							closeByte()
						}
					}
				} else {
					sclDownAt = e.at
				}
			}
			lastActivity = e.at
		case "sda":
			if e.t.level == nil {
				continue
			}
			lvl, _ := scl.levelAt(e.at)
			if active {
				if *e.t.level != lvl && lvl == idleLine {
					// SDA changes while SCL at idle: START or STOP
					if startFalling == (*e.t.level == false) {
						// START / repeated START
						if cur != nil {
							seq := e.seq
							out.diags = append(out.diags, model.Diagnostic{
								Code: "REPEATED_START", Severity: "warning", EdgeSeq: &seq, At: e.at,
								Message: "repeated START splits transactions; fragments are not merged",
							})
							finishFrame(e.at, "repeated_start")
						}
						startFrame(e.at, []int{e.seq}, cur != nil, "")
						active = true
						lastActivity = e.at
					} else {
						finishFrame(e.at, "stop")
						lastActivity = e.at
					}
				}
			} else if lvl == idleLine {
				if startFalling == (*e.t.level == false) {
					startFrame(e.at, []int{e.seq}, false, "")
					active = true
					lastActivity = e.at
				}
			}
			lastActivity = e.at
		}
	}
	if active {
		finishFrame(lastEventAt(ev), "end_of_capture")
	}
	return out
}

func lastEventAt(ev []mergedEv) float64 {
	if len(ev) == 0 {
		return 0
	}
	return ev[len(ev)-1].at
}

func defaultOr(o model.Orig, fallback float64) float64 {
	if o.OK {
		return o.SI
	}
	return fallback
}

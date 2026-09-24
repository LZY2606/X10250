package decode

import (
	"fmt"
	"sort"
)

type ev struct {
	t     float64
	ch    string // scl|sda
	level int
	id    string
}

func mergedEvents(scl, sda *Sig) []ev {
	var es []ev
	for i, t := range scl.Times {
		es = append(es, ev{t: t, ch: "scl", level: scl.Levels[i], id: scl.Edges[i].ID})
	}
	for i, t := range sda.Times {
		es = append(es, ev{t: t, ch: "sda", level: sda.Levels[i], id: sda.Edges[i].ID})
	}
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].t != es[j].t {
			return es[i].t < es[j].t
		}
		return es[i].ch < es[j].ch
	})
	return es
}

// clockPeriod estimates the SCL period from consecutive same-direction edges.
func clockPeriod(scl *Sig) float64 {
	var rise []float64
	for i, l := range scl.Levels {
		if l == 1 {
			rise = append(rise, scl.Times[i])
		}
	}
	if len(rise) < 3 {
		return 0
	}
	sort.Float64s(rise)
	var med []float64
	for i := 1; i < len(rise); i++ {
		med = append(med, rise[i]-rise[i-1])
	}
	sort.Float64s(med)
	return med[len(med)/2]
}

func decodeI2C(res *Result, sigs map[string]*Sig, p ParamsView, budget Budget) error {
	scl, sda := sigs["scl"], sigs["sda"]
	period := clockPeriod(scl)
	tol := 0.20
	gap := 0.0
	if q := p.GapThreshold(); q.OK && q.Value > 0 {
		gap = q.Value
	} else if period > 0 {
		gap = period * 10
	}
	sampleEdge := "rising"
	if p.ClockPolarity() == "falling" {
		sampleEdge = "falling"
	}

	es := mergedEvents(scl, sda)
	type frame struct {
		startT      float64
		rstart      bool
		bits        []Bit
		byteIdx     int
		evidence    []Evidence
		aborted     bool
		abortReason string
	}
	var txns []Transaction
	var globalEv []Evidence
	cur := (*frame)(nil)
	lastT := -1.0
	lastSCL, lastSDA := 0, 0
	bitCount := 0
	var bitWinStart float64
	gapOpen := false
	newTx := func(t float64, rep bool) *frame {
		return &frame{startT: t, rstart: rep}
	}
	pushTxn := func(f *frame, endT float64, invalid string) {
		var byts []Byte
		for i := 0; i < len(f.bits)/9; i++ {
			val := 0
			var bs []Bit
			for b := 0; b < 8; b++ {
				bit := f.bits[i*9+b]
				bit.Index = b
				bs = append(bs, bit)
				if bit.Value == 1 {
					val |= 1 << uint(7-b)
				}
			}
			ackBit := f.bits[i*9+8]
			ack := "ACK"
			if ackBit.Value == 1 {
				ack = "NACK"
			}
			byts = append(byts, Byte{
				Index: i, Value: val, Bits: bs,
				Start: bs[0].Start, End: ackBit.End,
				Ack: ack, AckAt: ackBit.SampleAt,
			})
		}
		txn := Transaction{
			Kind: "i2c", Index: len(txns),
			Start: f.startT, End: endT, Bytes: byts,
			ValidFrom: f.startT, ValidTo: endT,
			Invalid: invalid, Evidence: f.evidence,
		}
		if len(byts) == 0 {
			txn.Invalid = orStr(invalid, "no complete byte between START and STOP")
		}
		txns = append(txns, txn)
	}

	// We detect START/STOP when SCL is sampled high at an SDA edge. Data bits
	// are sampled on the chosen SCL edge while the bus is "owned" by a frame.
	for k := range es {
		if err := budget.Use(1); err != nil {
			return err
		}
		e := es[k]
		if lastT >= 0 && e.t-lastT > gap && period > 0 {
			// A data gap never splices: the open frame is finished invalid.
			if cur != nil {
				globalEv = append(globalEv, Evidence{Code: "data-gap", StartSec: lastT, EndSec: e.t,
					Detail:  fmt.Sprintf("idle gap %s exceeds gap threshold", fmtSec(e.t-lastT)),
					EdgeIDs: []string{e.id}})
				cur.evidence = append(cur.evidence, Evidence{Code: "data-gap", StartSec: lastT, EndSec: e.t,
					Detail: "frame closed; not spliced across the gap"})
				pushTxn(cur, lastT, "data gap before frame completed")
				cur = nil
				bitCount = 0
			}
			gapOpen = true
		}
		switch e.ch {
		case "sda":
			if lastSCL == 1 {
				if e.level == 0 && lastSDA == 1 {
					if cur != nil {
						globalEv = append(globalEv, Evidence{Code: "repeated-start", StartSec: e.t, EndSec: e.t,
							Detail: "SDA fell while SCL high without STOP", EdgeIDs: []string{e.id}})
						// restart closes nothing on the wire; mark current frame, keep bytes as one txn tail
						cur.evidence = append(cur.evidence, Evidence{Code: "repeated-start", StartSec: e.t, EndSec: e.t, Detail: "repeated START"})
						cur.rstart = true
						bitCount = 0
					} else {
						cur = newTx(e.t, false)
						gapOpen = false
					}
				} else if e.level == 1 && lastSDA == 0 && cur != nil {
					globalEv = append(globalEv, Evidence{Code: "stop", StartSec: e.t, EndSec: e.t, EdgeIDs: []string{e.id},
						Detail: "SDA rose while SCL high"})
					if bitCount%9 != 0 {
						cur.evidence = append(cur.evidence, Evidence{Code: "missing-clock-edge", StartSec: bitWinStart, EndSec: e.t,
							Detail: fmt.Sprintf("%d trailing bit(s) lack 9-clock framing", bitCount%9)})
						pushTxn(cur, e.t, "truncated by STOP mid-byte (missing clock edges)")
					} else {
						pushTxn(cur, e.t, "")
					}
					cur = nil
					bitCount = 0
				}
			}
		case "scl":
			// clock stretching: SCL held low longer than one period.
			if e.level == 1 && period > 0 {
				// find previous scl fall time via es scan (cheap enough, bounded).
				var prevFall float64 = -1
				for j := k - 1; j >= 0; j-- {
					if es[j].ch == "scl" {
						if es[j].level == 0 {
							prevFall = es[j].t
							break
						}
					}
				}
				if prevFall >= 0 && e.t-prevFall > period*1.5 {
					evd := Evidence{Code: "clock-stretch", StartSec: prevFall, EndSec: e.t,
						Detail:  fmt.Sprintf("SCL low %s > 1.5 periods (%s)", fmtSec(e.t-prevFall), fmtSec(period)),
						EdgeIDs: []string{e.id}}
					globalEv = append(globalEv, evd)
					if cur != nil {
						cur.evidence = append(cur.evidence, evd)
					}
				}
			}
			want := 1
			if sampleEdge == "falling" {
				want = 0
			}
			if cur != nil && e.level == want {
				if bitCount == 0 {
					bitWinStart = e.t
				}
				d := sda.LevelAt(e.t)
				note := ""
				// boundary sample: nearest SDA transition within tolerance window.
				var near float64 = -1
				var nearID string
				for j := k - 1; j >= 0; j-- {
					if es[j].ch == "sda" {
						near = es[j].t
						nearID = es[j].id
						break
					}
				}
				if near >= 0 && period > 0 && e.t-near >= 0 && e.t-near < period*tol {
					note = "boundary-sample"
					evd := Evidence{Code: "boundary-sample", StartSec: near, EndSec: e.t,
						Detail:  fmt.Sprintf("SDA change %ss before sample (< %d%% period)", fmtSec(e.t-near), int(tol*100)),
						EdgeIDs: []string{nearID, e.id}}
					globalEv = append(globalEv, evd)
					cur.evidence = append(cur.evidence, evd)
				}
				// find next edge to bound the bit window.
				endT := e.t + period
				for j := k + 1; j < len(es); j++ {
					if es[j].ch == "scl" {
						endT = es[j].t
						break
					}
				}
				cur.bits = append(cur.bits, Bit{
					Index:       bitCount,
					Value:       d,
					Start:       e.t,
					End:         endT,
					SampleAt:    e.t,
					ClockEdge:   e.id,
					DataEdgeIDs: sda.EdgesBetween(e.t-period, e.t+period/2),
					Note:        note,
				})
				bitCount++
				if bitCount%9 == 0 {
					bitWinStart = e.t
				}
			}
		}
		if e.ch == "scl" {
			lastSCL = e.level
		} else {
			lastSDA = e.level
		}
		lastT = e.t
	}
	// Unterminated frame: missing STOP / clock edges => incomplete candidate.
	if cur != nil {
		evd := Evidence{Code: "missing-clock-edge", StartSec: bitWinStart, EndSec: lastT,
			Detail: "capture ended without STOP; trailing bits cannot be framed"}
		globalEv = append(globalEv, evd)
		cur.evidence = append(cur.evidence, evd)
		pushTxn(cur, lastT, "capture ended without STOP")
	}
	_ = gapOpen

	key := "i2c:" + sampleEdge
	res.Evidence = globalEv
	conf := 1.0
	for _, e := range globalEv {
		switch e.Code {
		case "boundary-sample":
			conf -= 0.1
		case "missing-clock-edge", "data-gap":
			conf -= 0.25
		case "clock-stretch":
			conf -= 0.02
		}
	}
	if conf < 0.2 {
		conf = 0.2
	}
	res.Candidates = []Candidate{{
		Key: key, Label: fmt.Sprintf("I2C sample-on-%s", sampleEdge),
		Confidence: conf, Transactions: txns,
	}}
	return nil
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func fmtSec(v float64) string {
	switch {
	case v >= 1:
		return trimF(v) + "s"
	case v >= 1e-6:
		return trimF(v*1e6) + "us"
	default:
		return trimF(v*1e9) + "ns"
	}
}

func trimF(v float64) string {
	s := fmt.Sprintf("%.4f", v)
	for len(s) > 0 && (s[len(s)-1] == '0') {
		s = s[:len(s)-1]
	}
	for len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	if s == "" {
		return "0"
	}
	return s
}

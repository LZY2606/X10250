package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

type bitRec struct {
	val      int
	t        int64
	sclIdx   int
	boundary bool
}

type i2cOut struct {
	txns       []model.Transaction
	suspicious []int64
}

// runI2C executes one I2C decode pass. virtual contains times of assumed
// (missing) clock edges for the alternative hypothesis.
func runI2C(scl, sda []model.LEdge, p *model.Params, virtual map[int64]bool, cid, sclCh, sdaCh string) *i2cOut {
	out := &i2cOut{}
	initScl := initLevel(scl, true)
	initSda := initLevel(sda, true)

	type ev struct {
		t    int64
		kind int // 0=scl edge, 1=sda edge, 2=virtual clock
		lvl  int
		idx  int
	}
	evs := make([]ev, 0, len(scl)+len(sda)+len(virtual))
	for _, e := range scl {
		evs = append(evs, ev{e.T, 0, e.Lvl, e.Idx})
	}
	for _, e := range sda {
		evs = append(evs, ev{e.T, 1, e.Lvl, e.Idx})
	}
	for t := range virtual {
		evs = append(evs, ev{t, 2, 1, -1})
	}
	sort.SliceStable(evs, func(a, b int) bool {
		if evs[a].t != evs[b].t {
			return evs[a].t < evs[b].t
		}
		return evs[a].kind < evs[b].kind
	})

	sclLvl := initScl
	inTxn := false
	addrNext := false
	var bits []bitRec
	var cur *model.Transaction
	lastT := int64(-1) << 60
	started := false
	sclFallT := int64(-1)
	lastSclRise := int64(-1)
	sdaLowToggles := 0
	txnIdx := 0

	finish := func(status string, endT int64) {
		if cur == nil {
			return
		}
		if len(bits) > 0 {
			cur.Annotations = append(cur.Annotations, model.Annotation{
				Type: "partial_byte", AtNS: bits[0].t,
				Note: fmt.Sprintf("%d dangling bit(s) at transaction end", len(bits))})
		}
		cur.Status = status
		cur.EndNS = endT
		out.txns = append(out.txns, *cur)
		cur = nil
		bits = nil
		inTxn = false
	}

	emitByte := func() {
		grp := make([]bitRec, 9)
		copy(grp, bits[:9])
		bits = bits[9:]
		val := 0
		refs := make([]int, 0, 9)
		boundary := false
		for j := 0; j < 8; j++ {
			val = val<<1 | grp[j].val
		}
		for _, b := range grp {
			refs = append(refs, b.sclIdx)
			if b.boundary {
				boundary = true
			}
		}
		ack := grp[8].val == 0
		role := "data"
		if addrNext {
			role = "addr"
			addrNext = false
		}
		cur.Bytes = append(cur.Bytes, model.Byte{
			ID:           model.HashID("byt", cur.ID, len(cur.Bytes)),
			Index:        len(cur.Bytes),
			Value:        val,
			Role:         role,
			Ack:          &ack,
			BoundaryRisk: boundary,
			EdgeRefs:     refs,
			StartNS:      grp[0].t,
			EndNS:        grp[8].t,
		})
	}

	sample := func(t int64, idx int) {
		v := levelAt(sda, t, initSda)
		boundary := nearestDist(sda, t) <= p.SampleOffsetNS
		bits = append(bits, bitRec{val: v, t: t, sclIdx: idx, boundary: boundary})
		if len(bits) == 9 {
			emitByte()
		}
	}

	for _, e := range evs {
		if started && inTxn && e.t-lastT > p.GapThreshNS {
			cur.Annotations = append(cur.Annotations, model.Annotation{
				Type: "gap", AtNS: lastT, EndNS: e.t,
				Note: fmt.Sprintf("bus silent for %d ns without STOP", e.t-lastT)})
			finish("gap_truncated", lastT)
		}
		started = true
		switch e.kind {
		case 0, 2: // scl edge or virtual clock
			rising := e.lvl == 1
			if e.kind == 2 {
				rising = true
			}
			if rising {
				if sclFallT >= 0 && inTxn && e.t-sclFallT > p.StretchThreshNS {
					cur.Annotations = append(cur.Annotations, model.Annotation{
						Type: "clock_stretch", AtNS: sclFallT, EndNS: e.t,
						Note: fmt.Sprintf("SCL held low for %d ns", e.t-sclFallT)})
				}
				if virtual == nil && inTxn && sdaLowToggles >= 2 && lastSclRise >= 0 {
					out.suspicious = append(out.suspicious, (lastSclRise+e.t)/2)
				}
				sdaLowToggles = 0
				lastSclRise = e.t
				sclLvl = 1
				if inTxn {
					sample(e.t, e.idx)
				}
			} else {
				sclLvl = 0
				sclFallT = e.t
				sdaLowToggles = 0
			}
		case 1: // sda edge
			if sclLvl == 1 {
				if e.lvl == 0 { // START or repeated START
					if inTxn {
						cur.Annotations = append(cur.Annotations, model.Annotation{
							Type: "repeated_start", AtNS: e.t,
							Note: "repeated START keeps the transaction open"})
						if len(bits) > 0 {
							cur.Annotations = append(cur.Annotations, model.Annotation{
								Type: "partial_byte", AtNS: bits[0].t,
								Note: fmt.Sprintf("%d dangling bit(s) before repeated START", len(bits))})
						}
						bits = nil
						addrNext = true
					} else {
						inTxn = true
						cur = &model.Transaction{
							ID:      model.HashID("txn", cid, txnIdx),
							Kind:    "i2c",
							Lane:    sdaCh,
							Status:  "complete",
							StartNS: e.t,
						}
						txnIdx++
						addrNext = true
						bits = nil
					}
				} else { // STOP
					if inTxn {
						finish("complete", e.t)
					}
				}
			} else {
				sdaLowToggles++
			}
		}
		lastT = e.t
	}
	if inTxn {
		finish("truncated", lastT)
	}
	return out
}

func i2cConfidence(o *i2cOut) float64 {
	conf := 1.0
	for _, tx := range o.txns {
		for _, b := range tx.Bytes {
			if b.BoundaryRisk {
				conf -= 0.05
			}
		}
	}
	if conf < 0.3 {
		conf = 0.3
	}
	return round2(conf)
}

func i2cJudgments(cid string, o *i2cOut) []model.Judgment {
	var js []model.Judgment
	for _, tx := range o.txns {
		for _, a := range tx.Annotations {
			switch a.Type {
			case "repeated_start":
				js = append(js, model.Judgment{Rule: model.RuleRepeatedStart, Subject: tx.ID,
					Basis:   fmt.Sprintf("SDA fell while SCL high at %d ns inside transaction", a.AtNS),
					Adopted: true})
			case "clock_stretch":
				js = append(js, model.Judgment{Rule: model.RuleClockStretch, Subject: tx.ID,
					Basis:   fmt.Sprintf("SCL low for %d ns at %d ns; wait instead of abort", a.EndNS-a.AtNS, a.AtNS),
					Adopted: true})
			case "gap":
				js = append(js, model.Judgment{Rule: model.RuleNoStitchOnGap, Subject: tx.ID,
					Basis:   fmt.Sprintf("bus silent %d ns without STOP; later traffic is a separate transaction", a.EndNS-a.AtNS),
					Adopted: true})
			}
		}
	}
	return js
}

func decodeI2C(scl, sda []model.LEdge, p *model.Params, sclCh, sdaCh, decID string) ([]model.Candidate, []model.Judgment) {
	cidP := model.HashID("cnd", decID, "i2c", "as-recorded")
	primary := runI2C(scl, sda, p, nil, cidP, sclCh, sdaCh)
	if len(primary.txns) == 0 {
		return nil, nil
	}
	cands := []model.Candidate{{
		ID:           cidP,
		Protocol:     "i2c",
		Hypothesis:   "i2c/as-recorded",
		Confidence:   i2cConfidence(primary),
		Transactions: primary.txns,
	}}
	judgments := i2cJudgments(cidP, primary)

	if len(primary.suspicious) > 0 {
		virtual := map[int64]bool{}
		for _, t := range primary.suspicious {
			virtual[t] = true
		}
		cidA := model.HashID("cnd", decID, "i2c", "assume-missing-clock")
		alt := runI2C(scl, sda, p, virtual, cidA, sclCh, sdaCh)
		cands[0].Confidence = 0.55
		cands = append(cands, model.Candidate{
			ID:           cidA,
			Protocol:     "i2c",
			Hypothesis:   "i2c/assume-missing-clock",
			Confidence:   0.45,
			Transactions: alt.txns,
		})
		judgments = append(judgments, model.Judgment{
			Rule:    model.RuleMissingClock,
			Subject: cidA,
			Basis: fmt.Sprintf("SDA toggled >=2 times within one SCL low period at %v; both bit alignments retained",
				primary.suspicious),
			Adopted: true,
		})
		judgments = append(judgments, i2cJudgments(cidA, alt)...)
	}
	return cands, judgments
}

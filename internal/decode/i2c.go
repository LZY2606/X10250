package decode

import (
	"fmt"

	"edgebench/internal/model"
)

type i2cTx struct {
	startNs, endNs int64
	repeatedStart  bool
	truncatedByGap bool
	bits           []int
	bitTimes       []int64
	bitEdgeRefs    []int
	anns           []model.Annotation
}

func (t *i2cTx) toModel(proto string, dropPartial bool, idPrefix string) model.Transaction {
	tx := model.Transaction{
		StartNs:        t.startNs,
		EndNs:          t.endNs,
		RepeatedStart:  t.repeatedStart,
		TruncatedByGap: t.truncatedByGap,
		Annotations:    t.anns,
	}
	nBits := len(t.bits)
	full := nBits / 9
	rem := nBits % 9
	nBytes := full
	if rem > 0 && !dropPartial {
		nBytes++
	}
	for b := 0; b < nBytes; b++ {
		var val, nb int
		masked := false
		var ack *bool
		if b < full {
			nb = 9
		} else {
			nb = rem
			masked = true
		}
		for k := 0; k < 8; k++ {
			val <<= 1
			if k < nb && b*9+k < len(t.bits) {
				val |= t.bits[b*9+k]
			}
		}
		if nb == 9 {
			a := t.bits[b*9+8] == 0
			ack = &a
		}
		b0 := b * 9
		b1 := b*9 + nb - 1
		tx.Bytes = append(tx.Bytes, model.Byte{
			Value: val, Masked: masked, Ack: ack,
			StartNs: t.bitTimes[b0], EndNs: t.bitTimes[b1],
			EdgeIdx0: t.bitEdgeRefs[b0], EdgeIdx1: t.bitEdgeRefs[b1],
		})
	}
	return tx
}

// DecodeI2C decodes SDA/SCL. Repeated START splits segments but keeps them in
// order; clock stretching is annotated; a missing clock edge yields a second
// candidate with a masked byte; a data gap truncates the transaction and is
// never spliced across.
func DecodeI2C(p *Prepared, cfg *model.ProbeConfig) ([]*model.Candidate, []model.Diagnostic, *model.Diagnostic) {
	sda, ok1 := p.ByRole["sda"]
	scl, ok2 := p.ByRole["scl"]
	if !ok1 || !ok2 {
		missing := "sda"
		if !ok2 {
			missing = "scl"
		}
		return nil, nil, &model.Diagnostic{Code: model.CodeMissingChannel, Severity: "error",
			Message: fmt.Sprintf("i2c decode needs channel with role %q", missing), Channel: missing}
	}
	type ev struct {
		t     int64
		ch    string
		lvl   int
		ref   int
		isSCL bool
	}
	var evs []ev
	for i, t := range p.Times[sda] {
		evs = append(evs, ev{t, sda, p.Levels[sda][i], p.EdgeRefs[sda][i], false})
	}
	for i, t := range p.Times[scl] {
		evs = append(evs, ev{t, scl, p.Levels[scl][i], p.EdgeRefs[scl][i], true})
	}
	// deterministic: time asc; on ties SDA first (data settles before clock)
	for i := 1; i < len(evs); i++ {
		for j := i; j > 0 && (evs[j-1].t > evs[j].t || (evs[j-1].t == evs[j].t && evs[j-1].isSCL && !evs[j].isSCL)); j-- {
			evs[j-1], evs[j] = evs[j], evs[j-1]
		}
	}

	var txs []*i2cTx
	var diags []model.Diagnostic
	var cur *i2cTx
	sclLvl, sdaLvl := 1, 1
	lastSclFall := int64(-1)
	lastActivity := int64(-1)
	sawPartial := false

	finish := func(byGap bool, t int64) {
		if cur == nil {
			return
		}
		if byGap {
			cur.truncatedByGap = true
			cur.anns = append(cur.anns, model.Annotation{Type: model.CodeDataGap, TimeNs: t,
				Detail: "transaction truncated by data gap; bytes across the gap are not spliced"})
			diags = append(diags, model.Diagnostic{Code: model.CodeDataGap, Severity: "warn",
				Message: "data gap inside transaction; transaction closed, not spliced", TimeNs: t})
		}
		if rem := len(cur.bits) % 9; rem != 0 {
			sawPartial = true
			diags = append(diags, model.Diagnostic{Code: model.CodeMissingClock, Severity: "warn",
				Message: fmt.Sprintf("byte incomplete: %d data bits without 9th clock", rem),
				TimeNs:  cur.endNs, Channel: scl})
		}
		txs = append(txs, cur)
		cur = nil
	}

	for _, e := range evs {
		if cur != nil && lastActivity >= 0 && e.t-lastActivity > cfg.GapThresholdNs {
			finish(true, lastActivity)
		}
		if e.isSCL {
			if e.lvl == 1 { // rising
				if cur != nil {
					cur.bits = append(cur.bits, p.levelAt(sda, e.t, sdaLvl))
					cur.bitTimes = append(cur.bitTimes, e.t)
					cur.bitEdgeRefs = append(cur.bitEdgeRefs, e.ref)
					cur.endNs = e.t
				}
				sclLvl = 1
			} else { // falling
				if lastSclFall >= 0 && cfg.StretchThresholdNs > 0 && e.t-lastSclFall > cfg.StretchThresholdNs {
					// long low period ending now: slave held SCL low
					if cur != nil {
						cur.anns = append(cur.anns, model.Annotation{Type: model.CodeClockStretch,
							TimeNs: lastSclFall, DurNs: e.t - lastSclFall, ByteIdx: len(cur.bits) / 9})
					}
					diags = append(diags, model.Diagnostic{Code: model.CodeClockStretch, Severity: "info",
						Message: fmt.Sprintf("SCL held low %d ns (clock stretching)", e.t-lastSclFall),
						Channel: scl, TimeNs: lastSclFall})
				}
				lastSclFall = e.t
				sclLvl = 0
			}
		} else {
			if e.lvl == 0 && sclLvl == 1 { // START
				if cur != nil {
					finish(false, e.t)
					cur = &i2cTx{startNs: e.t, repeatedStart: true}
					cur.anns = append(cur.anns, model.Annotation{Type: model.CodeRepeatedStart, TimeNs: e.t})
					diags = append(diags, model.Diagnostic{Code: model.CodeRepeatedStart, Severity: "info",
						Message: "repeated START: new segment without STOP", TimeNs: e.t, Channel: sda})
				} else {
					cur = &i2cTx{startNs: e.t}
				}
			} else if e.lvl == 1 && sclLvl == 1 { // STOP
				finish(false, e.t)
			}
			sdaLvl = e.lvl
		}
		lastActivity = e.t
	}
	finish(false, 0)

	if len(txs) == 0 {
		return nil, diags, &model.Diagnostic{Code: model.CodeUnsolvable, Severity: "error",
			Message: "i2c: edges present but no START condition found", Channel: sda}
	}
	return i2cCandidates(txs, sawPartial), diags, nil
}

func i2cCandidates(txs []*i2cTx, sawPartial bool) []*model.Candidate {
	mk := func(dropPartial bool, conf float64, reasons []string) *model.Candidate {
		c := &model.Candidate{Protocol: "i2c", Confidence: conf, Reasons: reasons}
		for _, t := range txs {
			tx := t.toModel("i2c", dropPartial, "")
			if len(tx.Bytes) > 0 {
				b0 := tx.Bytes[0]
				rw := "W"
				if b0.Value&1 == 1 {
					rw = "R"
				}
				tx.Device = fmt.Sprintf("0x%02X/%s", b0.Value>>1, rw)
			} else {
				tx.Device = "unknown"
			}
			c.Txs = append(c.Txs, tx)
		}
		return c
	}
	reasons := []string{}
	for _, t := range txs {
		if t.repeatedStart {
			reasons = append(reasons, model.CodeRepeatedStart)
		}
		if t.truncatedByGap {
			reasons = append(reasons, model.CodeDataGap)
		}
		for _, a := range t.anns {
			if a.Type == model.CodeClockStretch {
				reasons = append(reasons, model.CodeClockStretch)
			}
		}
	}
	uniq := func(s []string) []string {
		seen := map[string]bool{}
		var out []string
		for _, x := range s {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
		return out
	}
	reasons = uniq(reasons)
	conf := 0.95
	if len(reasons) > 0 {
		conf = 0.85
	}
	primary := mk(true, conf, reasons)
	if !sawPartial {
		return []*model.Candidate{primary}
	}
	altReasons := append(append([]string{}, reasons...), model.CodeMissingClock)
	primary.Reasons = append(append([]string{}, reasons...), model.CodeMissingClock)
	primary.Confidence = 0.6
	alt := mk(false, 0.4, altReasons)
	return []*model.Candidate{primary, alt}
}

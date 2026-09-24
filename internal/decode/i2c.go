package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

type i2cTxnBuild struct {
	startNs     int64
	bits        []model.BitSample
	annotations []model.Annotation
	ambiguity   []int // bit indexes where a clock edge is missing
	periods     []int64
	lastSample  int64
	lowStart    int64
}

// DecodeI2C decodes I2C from scl/sda edges. It keeps multiple candidates for
// missing clock edges, annotates repeated START and clock stretching, and
// never stitches transactions across data gaps.
func DecodeI2C(in *Input) ([]model.Candidate, []model.Diagnostic, string) {
	var diags []model.Diagnostic
	scl := in.edges("scl")
	sda := in.edges("sda")
	if len(scl) == 0 || len(sda) == 0 {
		return nil, []model.Diagnostic{{Code: "empty_channel", Severity: "error",
			Position: "roles.scl", Message: "I2C decode requires edges on both scl and sda"}}, model.StatusCorruptInput
	}
	p := in.Params
	sampleOnRising := p.ClockPolarity != "falling"

	var txns []i2cTxnBuild
	cur := i2cTxnBuild{startNs: -1, lastSample: -1, lowStart: -1}
	inTxn := false
	sclLv := in.initial("scl")
	sdaLv := in.initial("sda")
	lastActivity := int64(-1)
	status := model.StatusOK

	closeTxn := func(endNs int64, why string) {
		if !inTxn {
			return
		}
		if why == "" {
			// STOP: the clock pulse framing the STOP condition does not
			// carry data; drop the incomplete trailing byte silently
			if rem := len(cur.bits) % 9; rem != 0 {
				cur.bits = cur.bits[:len(cur.bits)-rem]
			}
		}
		if why != "" {
			cur.annotations = append(cur.annotations, model.Annotation{
				Code: why, StartNs: endNs, EndNs: endNs,
				Detail: "transaction closed: " + why})
		}
		txns = append(txns, cur)
		cur = i2cTxnBuild{startNs: -1, lastSample: -1, lowStart: -1}
		inTxn = false
	}

	for _, e := range in.merged("scl", "sda") {
		if !in.spend(1) {
			return nil, diags, model.StatusBudgetExhausted
		}
		if inTxn && !p.AllowGapStitching && lastActivity >= 0 && e.TNs-lastActivity > p.GapThresholdNs {
			// data gap: close the transaction, never stitch across it
			closeTxn(lastActivity, "data_gap")
		}
		lastActivity = e.TNs

		if e.Ch == in.Config.Roles["sda"] {
			prev := sdaLv
			sdaLv = e.Level
			if sclLv == 1 {
				switch {
				case prev == 1 && e.Level == 0: // START
					if inTxn {
						if p.TreatRepeatedStartAsStop {
							closeTxn(e.TNs, "repeated_start_as_stop")
							inTxn = true
							cur.startNs = e.TNs
						} else {
							cur.annotations = append(cur.annotations, model.Annotation{
								Code: "repeated_start", StartNs: e.TNs, EndNs: e.TNs,
								Detail: "START without preceding STOP"})
							// a START condition invalidates an incomplete byte
							if rem := len(cur.bits) % 9; rem != 0 {
								cur.bits = cur.bits[:len(cur.bits)-rem]
							}
						}
					} else {
						inTxn = true
						cur.startNs = e.TNs
					}
				case prev == 0 && e.Level == 1: // STOP
					closeTxn(e.TNs, "")
				}
			}
			continue
		}
		// SCL edge
		prev := sclLv
		sclLv = e.Level
		isRising := prev == 0 && e.Level == 1
		isFalling := prev == 1 && e.Level == 0
		if isFalling {
			cur.lowStart = e.TNs
		}
		stretched := false
		if isRising && cur.lowStart >= 0 && e.TNs-cur.lowStart > p.StretchThresholdNs && inTxn {
			cur.annotations = append(cur.annotations, model.Annotation{
				Code: "clock_stretch", StartNs: cur.lowStart, EndNs: e.TNs,
				Detail: fmt.Sprintf("SCL held low %s", model.FormatNs(e.TNs-cur.lowStart))})
			cur.lowStart = -1
			stretched = true
		}
		isSample := (sampleOnRising && isRising) || (!sampleOnRising && isFalling)
		if !isSample || !inTxn {
			continue
		}
		if cur.lastSample >= 0 {
			period := e.TNs - cur.lastSample
			if len(cur.periods) >= 3 && !stretched {
				med := median(cur.periods)
				if med > 0 && period*5 > med*9 {
					cur.ambiguity = append(cur.ambiguity, len(cur.bits))
					cur.annotations = append(cur.annotations, model.Annotation{
						Code: "missing_clock_edge", StartNs: cur.lastSample, EndNs: e.TNs,
						Detail: fmt.Sprintf("SCL period %s exceeds 2x median %s", model.FormatNs(period), model.FormatNs(med))})
				}
			}
			cur.periods = append(cur.periods, period)
		}
		cur.lastSample = e.TNs
		cur.bits = append(cur.bits, model.BitSample{Value: sdaLv, ClockEdge: e.ID, TNs: e.TNs})
	}
	closeTxn(lastActivity, "")

	if len(txns) == 0 {
		if len(sda) > 1 && len(scl) < 2 {
			return nil, []model.Diagnostic{{Code: "no_clock", Severity: "error",
				Position: "roles.scl", Message: "data toggles but no usable clock edges: semantically unsolvable"}}, model.StatusUnsolvable
		}
		return nil, diags, status
	}

	// Build candidates. Each ambiguity position doubles the hypotheses
	// (insert assumed bit 0 or 1); capped at 2 ambiguities per transaction.
	type variant struct {
		assumptions []string
		txns        []model.Transaction
		penalty     float64
	}
	variants := []variant{{penalty: 1.0}}
	for ti, tb := range txns {
		if len(tb.ambiguity) > 2 {
			return nil, append(diags, model.Diagnostic{Code: "too_many_ambiguities", Severity: "error",
				Position: fmt.Sprintf("transactions[%d]", ti),
				Message:  "more than 2 missing clock edges in one transaction: semantically unsolvable"}), model.StatusUnsolvable
		}
		if len(tb.ambiguity) == 0 {
			for vi := range variants {
				variants[vi].txns = append(variants[vi].txns, buildI2CTxn(tb, nil))
			}
			continue
		}
		var next []variant
		for _, v := range variants {
			combos := 1 << uint(len(tb.ambiguity))
			for c := 0; c < combos; c++ {
				if !in.spend(1) {
					return nil, diags, model.StatusBudgetExhausted
				}
				nv := variant{assumptions: append([]string{}, v.assumptions...), penalty: v.penalty}
				inserts := map[int]int{}
				for ai, pos := range tb.ambiguity {
					bit := (c >> uint(ai)) & 1
					inserts[pos] = bit
					nv.assumptions = append(nv.assumptions,
						fmt.Sprintf("missing_clock_bit@%d=%d", pos, bit))
					nv.penalty *= 0.5
				}
				nv.txns = append(append([]model.Transaction{}, v.txns...), buildI2CTxn(tb, inserts))
				next = append(next, nv)
			}
		}
		variants = next
	}

	var cands []model.Candidate
	for _, v := range variants {
		c := model.Candidate{
			Decoder:      "i2c",
			Confidence:   v.penalty,
			Assumptions:  v.assumptions,
			Transactions: v.txns,
		}
		c.ID = candidateID("i2c", v.assumptions, v.txns)
		assignIDs(&c)
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		return cands[i].ID < cands[j].ID
	})
	return cands, diags, status
}

// buildI2CTxn groups bits into 9-bit bytes (8 data + ACK/NACK). inserts maps
// bit positions to assumed values for missing clock edges.
func buildI2CTxn(tb i2cTxnBuild, inserts map[int]int) model.Transaction {
	bits := append([]model.BitSample{}, tb.bits...)
	var positions []int
	for pos := range inserts {
		positions = append(positions, pos)
	}
	sort.Ints(positions)
	for _, pos := range positions {
		b := model.BitSample{Value: inserts[pos], Assumed: true}
		if pos > 0 && pos <= len(bits) {
			b.TNs = bits[pos-1].TNs
		} else if len(bits) > 0 {
			b.TNs = bits[len(bits)-1].TNs
		}
		if pos >= len(bits) {
			bits = append(bits, b)
		} else {
			bits = append(bits[:pos], append([]model.BitSample{b}, bits[pos:]...)...)
		}
	}
	t := model.Transaction{Bus: "i2c0", StartNs: tb.startNs, Annotations: tb.annotations}
	nBytes := len(bits) / 9
	for i := 0; i < nBytes; i++ {
		nb := model.Byte{Index: i, Role: "data"}
		if i == 0 || hasRepeatedStartBefore(tb.annotations, bits[i*9].TNs) {
			nb.Role = "address"
		}
		for k := 0; k < 9; k++ {
			bs := bits[i*9+k]
			if k < 8 {
				nb.Value = nb.Value<<1 | bs.Value
			}
			nb.Bits = append(nb.Bits, bs)
			if k == 0 {
				nb.StartNs = bs.TNs
			}
			nb.EndNs = bs.TNs
		}
		ack := bits[i*9+8].Value == 0
		nb.Ack = &ack
		t.Bytes = append(t.Bytes, nb)
	}
	if rem := len(bits) % 9; rem != 0 {
		t.Annotations = append(t.Annotations, model.Annotation{
			Code:    "partial_byte",
			StartNs: bits[len(bits)-rem].TNs, EndNs: bits[len(bits)-1].TNs,
			Detail: fmt.Sprintf("%d trailing bits do not form a complete byte", rem)})
	}
	if len(t.Bytes) > 0 {
		t.EndNs = t.Bytes[len(t.Bytes)-1].EndNs
	} else {
		t.EndNs = tb.startNs
	}
	return t
}

func hasRepeatedStartBefore(anns []model.Annotation, t int64) bool {
	for _, a := range anns {
		if a.Code == "repeated_start" && a.StartNs <= t {
			return true
		}
	}
	return false
}

func median(v []int64) int64 {
	s := append([]int64{}, v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}

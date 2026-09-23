package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// i2cFrame is one transaction frame delimited by START/STOP/gap.
type i2cFrame struct {
	startT, endT  int64
	repeatedStart bool
	endedBy       string // stop | gap | eof
	bits          []bitCell
	stretches     int
}

// decodeI2C decodes SDA/SCL edges into I2C candidates.
//
// Judgment rules (emitted as machine-readable Judgments):
//   - repeated_start: frame continues, framing restarts;
//   - clock_stretch: tolerated, recorded with position;
//   - data_gap: frame closed at the gap, never stitched;
//   - missing clock edge (partial bit count at frame end): both the
//     "drop partial" and "keep partial" framings are kept as candidates;
//   - boundary sample: both bit values are kept as candidates.
func decodeI2C(sda, scl []nedge, p model.ParamSet) ([]Candidate, []model.Diagnostic, []model.Judgment) {
	var diags []model.Diagnostic
	var judges []model.Judgment
	if len(scl) == 0 || len(sda) == 0 {
		return nil, nil, nil
	}

	// Merge both channels into one timeline; SCL wins ties so that a data
	// change exactly on a clock edge is seen after the sample.
	type ev struct {
		t     int64
		isSCL bool
		level int
		id    string
	}
	merged := make([]ev, 0, len(sda)+len(scl))
	for _, e := range sda {
		merged = append(merged, ev{t: e.t, level: e.level, id: e.id})
	}
	for _, e := range scl {
		merged = append(merged, ev{t: e.t, isSCL: true, level: e.level, id: e.id})
	}
	// insertion sort by (t, isSCL desc) — inputs are nearly sorted
	for i := 1; i < len(merged); i++ {
		for j := i; j > 0; j-- {
			a, b := merged[j-1], merged[j]
			if a.t < b.t || (a.t == b.t && a.isSCL && !b.isSCL) {
				break
			}
			merged[j-1], merged[j] = merged[j], merged[j-1]
		}
	}

	sdaLvl := 1 - sda[0].level
	sclLvl := 1 - scl[0].level

	var frames []*i2cFrame
	var cur *i2cFrame
	var lastSCLFall, lastActivity int64 = -1, -1

	closeFrame := func(why string, t int64) {
		if cur == nil {
			return
		}
		cur.endedBy = why
		cur.endT = t
		frames = append(frames, cur)
		cur = nil
	}

	for _, e := range merged {
		if cur != nil && lastActivity >= 0 && e.t-lastActivity > p.GapThresholdPs {
			closeFrame("gap", lastActivity)
			diags = append(diags, model.Diagnostic{
				Code: "data_gap", EdgeID: e.id, Tps: e.t, HasPos: true,
				Message: fmt.Sprintf("data gap of %s at %d ps; transaction closed, not stitched",
					model.FormatPs(e.t-lastActivity), e.t),
			})
			judges = append(judges, model.Judgment{
				Rule: "data_gap", Action: "close_transaction_no_stitch",
				Subject: fmt.Sprintf("frame@%d", len(frames)-1),
				Detail:  map[string]string{"gap_ps": fmt.Sprint(e.t - lastActivity), "at_ps": fmt.Sprint(e.t)},
			})
		}
		if e.isSCL {
			if e.level == 1 { // rising: sample point
				if lastSCLFall >= 0 && e.t-lastSCLFall > p.I2CStretchThresholdPs {
					diags = append(diags, model.Diagnostic{
						Code: "clock_stretch", EdgeID: e.id, Tps: e.t, HasPos: true,
						Message: fmt.Sprintf("SCL held low for %s (clock stretching)", model.FormatPs(e.t-lastSCLFall)),
					})
					judges = append(judges, model.Judgment{
						Rule: "clock_stretch", Action: "tolerate_and_record",
						Subject: e.id,
						Detail:  map[string]string{"low_ps": fmt.Sprint(e.t - lastSCLFall), "at_ps": fmt.Sprint(e.t)},
					})
					if cur != nil {
						cur.stretches++
					}
				}
				if cur != nil {
					cur.bits = append(cur.bits, bitCell{
						v: sdaLvl, amb: ambNear(sda, e.t, p.I2CBoundaryWindowPs),
						t: e.t, edgeID: e.id,
					})
				}
			} else {
				lastSCLFall = e.t
			}
		} else {
			sdaLvl = e.level
			if sclLvl == 1 {
				if e.level == 0 { // START
					if cur != nil {
						cur.repeatedStart = true
						cur.bits = nil
						diags = append(diags, model.Diagnostic{
							Code: "repeated_start", EdgeID: e.id, Tps: e.t, HasPos: true,
							Message: "repeated START: transaction continues, framing restarted",
						})
						judges = append(judges, model.Judgment{
							Rule: "repeated_start", Action: "continue_transaction_restart_framing",
							Subject: e.id, Detail: map[string]string{"at_ps": fmt.Sprint(e.t)},
						})
					} else {
						cur = &i2cFrame{startT: e.t}
					}
				} else { // STOP
					closeFrame("stop", e.t)
				}
			}
		}
		if e.isSCL {
			sclLvl = e.level
		}
		lastActivity = e.t
	}
	closeFrame("eof", lastActivity)

	// Split into candidates. Structural ambiguity: frames whose bit count
	// is not a whole number of 9-bit cells (missing clock edge).
	hasPartial := false
	nAmb := 0
	for _, f := range frames {
		if len(f.bits)%9 != 0 {
			hasPartial = true
		}
		for _, b := range f.bits {
			if b.amb {
				nAmb++
			}
		}
	}

	structural := []string{"drop_partial"}
	if hasPartial {
		structural = append(structural, "keep_partial")
		judges = append(judges, model.Judgment{
			Rule: "missing_clock_edge", Action: "keep_multiple_candidates",
			Subject: "i2c",
			Detail:  map[string]string{"framings": "drop_partial,keep_partial"},
		})
	}
	if nAmb > 0 {
		judges = append(judges, model.Judgment{
			Rule: "boundary_sample", Action: "keep_multiple_candidates",
			Subject: "i2c",
			Detail:  map[string]string{"ambiguous_bits": fmt.Sprint(nAmb)},
		})
	}

	budget := p.MaxCandidates
	var out []Candidate
	for _, framing := range structural {
		assigns := expandAmb(nAmb, budget)
		for _, assign := range assigns {
			cand := Candidate{Bus: "i2c", Confidence: 1.0}
			ai := 0
			for fi, f := range frames {
				txn := Txn{Kind: "i2c", StartPs: f.startT, EndPs: f.endT, Meta: map[string]string{
					"ended_by": f.endedBy,
				}}
				if f.repeatedStart {
					txn.Meta["repeated_start"] = "yes"
				}
				if f.stretches > 0 {
					txn.Meta["clock_stretch"] = fmt.Sprint(f.stretches)
				}
				if f.endedBy == "gap" {
					txn.Meta["gap_not_stitched"] = "yes"
				}
				nbits := len(f.bits)
				if framing == "drop_partial" {
					nbits -= nbits % 9
				}
				cells := nbits / 9
				rem := nbits % 9
				for c := 0; c < cells; c++ {
					bits := f.bits[c*9 : c*9+9]
					val := 0
					for i := 0; i < 8; i++ {
						v := bits[i].v
						if bits[i].amb {
							if assign[ai] == 1 {
								v = 1 - bits[i].v
							}
							ai++
						}
						val = val<<1 | v
					}
					ack := "NACK"
					if bits[8].v == 0 {
						ack = "ACK"
					}
					txn.Bytes = append(txn.Bytes, Byte{
						Value: val, HasValue: true, Ack: ack, Valid: true,
						FirstEdgeID: bits[0].edgeID, LastEdgeID: bits[8].edgeID,
					})
				}
				if framing == "keep_partial" && rem > 0 {
					bits := f.bits[cells*9 : nbits]
					val := 0
					for _, b := range bits {
						v := b.v
						if b.amb {
							if assign[ai] == 1 {
								v = 1 - b.v
							}
							ai++
						}
						val = val<<1 | v
					}
					txn.Bytes = append(txn.Bytes, Byte{
						Value: val, HasValue: true, Valid: false,
						InvalidReason: fmt.Sprintf("missing clock edge: only %d of 9 bits present", rem),
						FirstEdgeID:   bits[0].edgeID, LastEdgeID: bits[len(bits)-1].edgeID,
					})
				}
				if len(txn.Bytes) > 0 {
					addr := txn.Bytes[0].Value
					rw := "W"
					if addr&1 == 1 {
						rw = "R"
					}
					txn.Device = fmt.Sprintf("0x%02X/%s", addr>>1, rw)
					txn.Meta["frame"] = fmt.Sprint(fi)
					cand.Txns = append(cand.Txns, txn)
				}
			}
			if len(cand.Txns) == 0 {
				continue
			}
			cand.Confidence = 1.0
			for i := 0; i < nAmb; i++ {
				cand.Confidence *= 0.85
			}
			if framing == "keep_partial" {
				cand.Confidence *= 0.7
				cand.Note = "framing=keep_partial"
			} else if hasPartial {
				cand.Note = "framing=drop_partial"
			}
			if nAmb > 0 {
				if cand.Note != "" {
					cand.Note += " "
				}
				cand.Note += fmt.Sprintf("boundary_bits=%d", nAmb)
			}
			out = append(out, cand)
		}
		budget -= len(out)
		if budget <= 0 {
			break
		}
	}
	return out, diags, judges
}

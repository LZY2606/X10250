package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// decodeI2C runs the I2C decoder. Sampling follows the configured clock
// polarity; repeated START opens a new transaction without STOP; clock
// stretching is tolerated and recorded; a missing clock edge forks
// hypotheses; a data gap closes the transaction and is never stitched.
func decodeI2C(channels []model.Channel, edges []model.Edge, levels []int, p Params) Result {
	sdaCh := roleOf(channels, "sda")
	sclCh := roleOf(channels, "scl")
	if len(sdaCh) == 0 || len(sclCh) == 0 {
		return Result{Diagnostics: []Diag{{
			Position: "probe", Code: "missing_role",
			Message: "i2c decode requires sda and scl probe roles", Severity: "error",
		}}}
	}
	sda, scl := sdaCh[0], sclCh[0]

	hypo0 := &i2cHypo{reasons: map[string]bool{}, conf: 1.0}
	hypos := []*i2cHypo{hypo0}
	var diags []Diag

	sclLevel, sdaLevel := 0, 0
	lastEdgeTime := int64(-1)
	lastSclFall := int64(-1)
	sdaChangedLow := false // sda moved while scl low with no scl edge since

	sampleOnRising := p.ClockPolarity != "falling"

	for i, e := range edges {
		if lastEdgeTime >= 0 && e.TimeNS-lastEdgeTime > p.GapThresholdNS {
			// Data gap: close every open transaction; never stitch across.
			for _, h := range hypos {
				if h.open {
					h.reasons[ReasonDataGap] = true
					h.closeTxn(&diags, i-1)
					h.gap = true
				}
			}
		}
		lastEdgeTime = e.TimeNS
		lv := levels[i]

		switch e.Channel {
		case scl:
			if lv == 1 && sclLevel == 0 {
				// SCL rising.
				if lastSclFall >= 0 && e.TimeNS-lastSclFall > p.StretchThresholdNS {
					for _, h := range hypos {
						if h.open {
							h.reasons[ReasonClockStretch] = true
						}
					}
				}
				if sampleOnRising {
					for _, h := range hypos {
						if h.open {
							h.pushBit(sdaLevel, i, edges)
						}
					}
				}
			} else if lv == 0 && sclLevel == 1 {
				lastSclFall = e.TimeNS
				if !sampleOnRising {
					for _, h := range hypos {
						if h.open {
							h.pushBit(sdaLevel, i, edges)
						}
					}
				}
			}
			sclLevel = lv
			sdaChangedLow = false
		case sda:
			if sclLevel == 1 {
				if lv == 0 {
					// START (or repeated START while a transaction is open).
					for _, h := range hypos {
						if h.open {
							h.reasons[ReasonRepeatedStart] = true
							h.closeTxn(&diags, i)
						}
						h.open = true
						h.txStart = len(h.bytes)
						h.txEdge = i
						h.dev = "i2c:?"
						h.kind = "write"
					}
				} else {
					// STOP.
					for _, h := range hypos {
						h.closeTxn(&diags, i)
					}
				}
				sdaChangedLow = false
			} else {
				// SDA moved while SCL low. If it already moved since the last
				// SCL edge, a clock edge is missing: fork hypotheses.
				if sdaChangedLow {
					var forked []*i2cHypo
					for _, h := range hypos {
						if !h.open {
							continue
						}
						h.reasons[ReasonMissingClock] = true
						h.conf *= 0.5
						alt := h.clone()
						// Hypothesis B: the dropped clock sampled the previous
						// SDA value as an extra bit before the next sample.
						alt.bits = append(alt.bits, 1-lv)
						alt.bitEdge = append(alt.bitEdge, i)
						if len(alt.bits) == 9 {
							alt.pushBit(alt.bits[8], i, edges)
							alt.bits = nil
							alt.bitEdge = nil
						}
						forked = append(forked, alt)
					}
					hypos = append(hypos, forked...)
				}
				sdaChangedLow = true
			}
			sdaLevel = lv
		}
	}
	for _, h := range hypos {
		h.closeTxn(&diags, len(edges)-1)
	}

	var cands []Candidate
	for _, h := range hypos {
		if len(h.bytes) == 0 {
			continue
		}
		cands = append(cands, Candidate{
			Protocol:   "i2c",
			Confidence: h.conf,
			Reasons:    sortedReasons(h.reasons),
			Detail:     fmt.Sprintf("i2c sample-on-%s", p.ClockPolarity),
			StartEdge:  h.txns[0].EdgeStart,
			EndEdge:    h.txns[len(h.txns)-1].EdgeEnd,
			Bytes:      h.bytes,
			Txns:       h.txns,
		})
	}
	return Result{Diagnostics: diags, Candidates: cands}
}

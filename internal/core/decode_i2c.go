package core

import (
	"fmt"
	"sort"
)

type i2cBit struct {
	v       int
	t       float64
	sclEdge string
	sdaEdge string
	altV    int // >=0: an alternative bit (missing clock edge hypothesis)
	altT    float64
	altEdge string
}

type openI2C struct {
	start float64
	bits  []i2cBit
	diags []Diagnostic
}

func decodeI2C(cap *Capture, edges []Edge, p Params) ([]Transaction, []Ambiguity, []Diagnostic) {
	sdaChans := cap.ChannelByRole("SDA")
	sclChans := cap.ChannelByRole("SCL")
	if len(sdaChans) == 0 || len(sclChans) == 0 {
		return nil, nil, nil // protocol not configured
	}
	sda, scl := &sdaChans[0], &sclChans[0]

	// Nominal SCL period from rising edges, used for clock-stretch and
	// missing-edge detection.
	var rises []float64
	prevSCL := -1
	for _, e := range channelEdges(edges, scl.Alias) {
		l := interpretedLevel(scl, e.Level)
		if prevSCL == 0 && l == 1 {
			rises = append(rises, e.Time.Value)
		}
		prevSCL = l
	}
	if len(rises) < 2 {
		// Semantic dead-end specific to this protocol: SCL exists but
		// never toggles a clock, so no byte can be framed.
		return nil, nil, []Diagnostic{{
			Code:    DiagSemanticUnsolvable,
			Message: "I2C SCL has no clocking edges",
			Channel: scl.Alias,
		}}
	}
	var periods []float64
	for i := 1; i < len(rises); i++ {
		periods = append(periods, rises[i]-rises[i-1])
	}
	nomPeriod := median(periods)
	stretchTol := p.StretchTolUs * 1e-6
	gapTol := p.GapThresholdUs * 1e-6

	var txns []Transaction
	var ambs []Ambiguity
	var cur *openI2C
	lastBus := -1.0
	var lastSCLFall float64
	var prevRise float64
	havePrevRise := false
	sclLevel, sdaLevel := -1, -1
	var lastSDAEdge string

	closeTxn := func(end float64, status string, extra ...Diagnostic) {
		if cur == nil {
			return
		}
		ds := append(cur.diags, extra...)
		expanded := expandI2CBits(cur.start, end, status, ds, cur.bits)
		if len(expanded) == 1 {
			txns = append(txns, expanded[0])
		} else {
			scope := fmt.Sprintf("i2c@%dns", int64(cur.start*1e9))
			amb := Ambiguity{Scope: scope, Reason: DiagMissingClockEdge}
			for _, t := range expanded {
				reasons := []string{DiagMissingClockEdge}
				conf := 1.0 / float64(len(expanded))
				c := Candidate{
					Confidence:  conf,
					Reasons:     reasons,
					Assumptions: map[string]string{"missing_clock_edge": t.ID},
					Transaction: t,
				}
				c.ID = HashOf("cand", struct {
					S string
					T Transaction
				}{scope, t})
				amb.Candidates = append(amb.Candidates, c)
			}
			if tiedScores(amb.Candidates) {
				for i := range amb.Candidates {
					amb.Candidates[i].Reasons = append(amb.Candidates[i].Reasons, DiagTiedCandidates)
				}
				ds = append(ds, Diagnostic{Code: DiagTiedCandidates, Message: "equal-confidence decode candidates retained", Time: cur.start, Unit: "s"})
			}
			amb.ID = HashOf("amb", amb)
			ambs = append(ambs, amb)
		}
		cur = nil
	}

	for _, e := range edges {
		if e.Channel != sda.Alias && e.Channel != scl.Alias {
			continue
		}
		t := e.Time.Value
		if cur != nil && t-lastBus > gapTol {
			closeTxn(lastBus, "gap_terminated", Diagnostic{
				Code:    DiagDataGap,
				Message: fmt.Sprintf("%.0fus silence without STOP; transaction not spliced", (t-lastBus)*1e6),
				Time:    lastBus,
				Unit:    "s",
			})
		}
		lastBus = t

		switch {
		case e.Channel == sda.Alias:
			newL := interpretedLevel(sda, e.Level)
			if sclLevel == 1 && sdaLevel == 1 && newL == 0 {
				// SDA falling while SCL high: START (or repeated START).
				if cur != nil {
					closeTxn(t, "closed_by_repeated_start", Diagnostic{
						Code:    DiagRepeatedStart,
						Message: "repeated START without preceding STOP",
						Time:    t,
						Unit:    "s",
					})
				}
				cur = &openI2C{start: t}
			} else if sclLevel == 1 && sdaLevel == 0 && newL == 1 && cur != nil {
				// SDA rising while SCL high: STOP.
				closeTxn(t, "ok", Diagnostic{Code: "stop", Message: "STOP", Time: t, Unit: "s"})
			}
			sdaLevel = newL
			lastSDAEdge = e.ID
		case e.Channel == scl.Alias:
			newL := interpretedLevel(scl, e.Level)
			if sclLevel == 1 && newL == 0 {
				lastSCLFall = t
			}
			if newL == 1 && sclLevel == 0 && cur != nil {
				// Rising clock edge: sample SDA.
				if t-lastSCLFall > stretchTol {
					cur.diags = append(cur.diags, Diagnostic{
						Code:    DiagClockStretching,
						Message: fmt.Sprintf("SCL held low %.0fus (clock stretching)", (t-lastSCLFall)*1e6),
						Time:    lastSCLFall,
						Unit:    "s",
					})
				} else if havePrevRise && t-prevRise > 2.2*nomPeriod {
					// Long clock cell. If SDA moved in the middle a
					// clock edge may have been missed: two legal
					// readings are retained instead of guessing.
					if mid := lastSDAChangeBetween(edges, sda, prevRise, t); mid != nil {
						cur.bits = append(cur.bits, i2cBit{
							v: sdaLevel, t: t, sclEdge: e.ID, sdaEdge: lastSDAEdge,
							altV: interpretedLevel(sda, mid.Level), altT: mid.Time.Value, altEdge: mid.ID,
						})
						cur.diags = append(cur.diags, Diagnostic{
							Code:    DiagMissingClockEdge,
							Message: fmt.Sprintf("clock cell %.1fx nominal with mid-cell SDA change", (t-prevRise)/nomPeriod),
							Time:    prevRise,
							Unit:    "s",
						})
						prevRise = t
						sclLevel = newL
						continue
					}
				}
				cur.bits = append(cur.bits, i2cBit{v: sdaLevel, t: t, sclEdge: e.ID, sdaEdge: lastSDAEdge, altV: -1})
				prevRise = t
				havePrevRise = true
			}
			sclLevel = newL
		}
	}
	if cur != nil {
		closeTxn(edges[len(edges)-1].Time.Value, "incomplete", Diagnostic{
			Code:    "no_stop",
			Message: "capture ended without STOP",
			Time:    cur.start,
			Unit:    "s",
		})
	}
	sort.SliceStable(txns, func(i, j int) bool { return txns[i].Start < txns[j].Start })
	return txns, ambs, nil
}

func lastSDAChangeBetween(edges []Edge, sda *ChannelSpec, from, to float64) *Edge {
	var found *Edge
	for i := range edges {
		e := &edges[i]
		if e.Channel != sda.Alias || e.Time.Value <= from || e.Time.Value >= to {
			continue
		}
		found = e
	}
	return found
}

func tiedScores(cs []Candidate) bool {
	if len(cs) < 2 {
		return false
	}
	first := cs[0].Confidence
	for _, c := range cs[1:] {
		if c.Confidence != first {
			return false
		}
	}
	return true
}

// expandI2CBits expands the alternative bit readings at ambiguous
// positions into separate, fully-decoded transactions.
func expandI2CBits(start, end float64, status string, diags []Diagnostic, bits []i2cBit) []Transaction {
	altIdx := []int{}
	for i, b := range bits {
		if b.altV >= 0 {
			altIdx = append(altIdx, i)
		}
	}
	combos := 1 << uint(len(altIdx))
	var out []Transaction
	for c := 0; c < combos; c++ {
		vals := make([]int, 0, len(bits)+len(altIdx))
		times := make([]float64, 0, cap(vals))
		edgeIDs := make([][]string, 0, cap(vals))
		mask := 0
		for _, b := range bits {
			idx := -1
			if b.altV >= 0 {
				idx = mask
				mask++
			}
			// Observed hypothesis: one bit at the actual clock.
			vals = append(vals, b.v)
			times = append(times, b.t)
			edgeIDs = append(edgeIDs, nonEmpty(b.sclEdge, b.sdaEdge))
			// Missing-clock hypothesis: insert the mid-cell bit first.
			if b.altV >= 0 && (c&(1<<uint(idx)) != 0) {
				vals = append(vals[:len(vals)-1], b.altV, b.v)
				times = append(times[:len(times)-1], b.altT, b.t)
				edgeIDs = append(edgeIDs[:len(edgeIDs)-1], nonEmpty(b.altEdge), nonEmpty(b.sclEdge, b.sdaEdge))
			}
		}
		out = append(out, assembleI2CTxn(start, end, status, diags, vals, times, edgeIDs))
	}
	return out
}

func nonEmpty(ids ...string) []string {
	var out []string
	for _, id := range ids {
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func assembleI2CTxn(start, end float64, status string, diags []Diagnostic, vals []int, times []float64, edgeIDs [][]string) Transaction {
	var bytes []Byte
	device := ""
	nFrames := len(vals) / 9
	for f := 0; f < nFrames; f++ {
		v := 0
		var refs []string
		for k := 0; k < 8; k++ {
			v = (v << 1) | vals[f*9+k]
			refs = append(refs, edgeIDs[f*9+k]...)
		}
		ack := vals[f*9+8] == 0
		refs = append(refs, edgeIDs[f*9+8]...)
		if f == 0 {
			device = fmt.Sprintf("0x%02X(%c)", v>>1, map[bool]byte{true: 'W', false: 'R'}[v&1 == 0])
		}
		bytes = append(bytes, makeByte("", f, v, &ack, dedupe(refs), times[f*9], times[f*9+8]))
	}
	if r := len(vals) - nFrames*9; r > 0 {
		v := 0
		var refs []string
		for k := 0; k < r; k++ {
			v = (v << 1) | vals[nFrames*9+k]
			refs = append(refs, edgeIDs[nFrames*9+k]...)
		}
		b := makeByte("", nFrames, v<<(8-r), nil, dedupe(refs), times[nFrames*9], times[len(times)-1])
		b.InvalidReason = "partial_frame_uncertain"
		bytes = append(bytes, b)
	}
	t := finishTxn("i2c", device, start, end, bytes, status, diags)
	for i := range bytes {
		bytes[i].ID = HashOf("byt", struct {
			T string
			I int
			V int
			E []string
		}{t.ID, bytes[i].Index, bytes[i].Value, bytes[i].EdgeIDs})
	}
	return t
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

package decoder

import (
	"fmt"
	"sort"

	"edge-decoder-review/internal/model"
)

func analyzeI2C(c model.Capture, bus model.BusConfig, traces map[string]*trace) model.BusAnalysis {
	sclAlias := bus.Signals["scl"]
	sdaAlias := bus.Signals["sda"]
	ba := model.BusAnalysis{BusID: bus.ID, Protocol: "i2c"}
	scl := withClockPolarity(traces[sclAlias], bus.ClockPolarity == "inverted")
	sda := traces[sdaAlias]
	if scl == nil || sda == nil {
		ba.InvalidReasons = append(ba.InvalidReasons, diagnostic("MISSING_BUS_TRACE", "error", "probe.buses."+bus.ID, "I2C signal trace is missing", sclAlias, sdaAlias))
		return ba
	}

	type boundary struct {
		start, stop point
		repeated    []point
	}
	var boundaries []boundary
	var active *boundary
	for _, p := range sda.changes() {
		sclLevel, _ := scl.at(p.t)
		if sclLevel && !p.logical && active == nil {
			active = &boundary{start: p}
		} else if sclLevel && !p.logical && active != nil {
			active.repeated = append(active.repeated, p)
		} else if sclLevel && p.logical && active != nil {
			active.stop = p
			boundaries = append(boundaries, *active)
			active = nil
		}
	}
	if active != nil {
		ba.InvalidReasons = append(ba.InvalidReasons, diagnostic("I2C_MISSING_STOP", "error", "bus."+bus.ID, "START has no STOP", active.start.edge.ID))
		boundaries = append(boundaries, *active)
	}

	samplePeriod := 0.0
	if c.SampleRate.Valid && c.SampleRate.Value > 0 {
		samplePeriod = 1 / c.SampleRate.Value
	}
	for _, b := range boundaries {
		end := 1e300
		if b.stop.edge.ID != "" {
			end = b.stop.t
		}
		var clocks []point
		for _, p := range scl.changes() {
			if p.t >= b.start.t && p.t <= end {
				clocks = append(clocks, p)
			}
		}
		var falls, rises []point
		for _, p := range clocks {
			if !p.logical {
				falls = append(falls, p)
			} else {
				rises = append(rises, p)
			}
		}
		ev := uniqueStrings([]string{b.start.edge.ID, b.stop.edge.ID})
		missing := detectI2CMissingEdges(falls, rises)
		groups, gapEdges := groupClockSamples(rises, c.SampleRate.Value, b.start.t, end)
		stretch := detectClockStretch(falls, rises, c.SampleRate.Value)
		reasons := append([]model.Diagnostic{}, missing...)
		if len(gapEdges) > 0 {
			reasons = append(reasons, diagnostic("I2C_DATA_GAP", "warning", "bus."+bus.ID+".data_gap", "SCL idle interval exceeds the valid data gap; fragments are not joined into one transaction", gapEdges...))
		}
		for _, eid := range stretch {
			reasons = append(reasons, diagnostic("I2C_CLOCK_STRETCHING", "warning", "bus."+bus.ID+".clock_stretching", "SCL low interval indicates clock stretching", eid))
		}
		for _, rp := range b.repeated {
			reasons = append(reasons, diagnostic("I2C_REPEATED_START", "warning", "bus."+bus.ID+".repeated_start", "SDA falls while SCL is high before a STOP", rp.edge.ID))
		}
		ba.ValidIntervals = append(ba.ValidIntervals, model.Interval{
			ID:    stableID("i2c-interval", bus.ID, b.start.edge.ID, b.stop.edge.ID),
			Start: q(b.start.t), End: q(end), Status: ternary(len(reasons) == 0, "valid", "conditional"),
			Reason: "I2C START to STOP interval; time base is seconds from capture origin", EdgeIDs: ev,
		})

		candidateModes := []string{"literal-fragment"}
		if len(missing) > 0 {
			candidateModes = []string{"literal-fragment", "resynchronized"}
		}
		for modeIndex, mode := range candidateModes {
			cand := model.Candidate{ID: stableID("i2c-candidate", bus.ID, b.start.edge.ID, b.stop.edge.ID, mode),
				Key: mode, Label: "I2C " + mode, Protocol: "i2c", RuleVersion: bus.DecoderVersion,
				Confidence: 0.72 - float64(modeIndex)*0.18 - 0.12*float64(len(reasons)), Status: "legal",
				Reason:          "all legal START/STOP interpretations retained when evidence is not unique",
				EvidenceEdgeIDs: ev, Diagnostics: reasons}
			if cand.Confidence < .25 {
				cand.Confidence = .25
			}
			for gi, group := range groups {
				if len(group) < 9 && mode == "resynchronized" {
					continue
				}
				bs, txStatus, txReason := decodeI2CByteGroup(bus, sda, group, gi, b, cand.ID, mode)
				cand.Bytes = append(cand.Bytes, bs...)
				if len(bs) > 0 {
					first, last := bs[0], bs[len(bs)-1]
					tx := model.Transaction{ID: stableID("i2c-tx", cand.ID, fmt.Sprint(gi)), BusID: bus.ID, Protocol: "i2c",
						Kind:      ternaryString(len(b.repeated) > 0, "i2c-repeated-start-fragment", "i2c-transaction"),
						StartTime: first.StartTime, EndTime: last.EndTime, StartEdgeID: first.ClockEdgeIDs[0],
						Status: txStatus, Reason: txReason, CandidateID: cand.ID}
					for _, by := range bs {
						tx.ByteIDs = append(tx.ByteIDs, by.ID)
					}
					cand.EvidenceEdgeIDs = append(cand.EvidenceEdgeIDs, tx.StartEdgeID)
					ba.Transactions = append(ba.Transactions, tx)
				}
			}
			if len(b.repeated) > 0 {
				cand.Reason += "; repeated START is preserved"
			}
			if len(gapEdges) > 0 {
				cand.Reason += "; data gaps remain separate fragments"
			}
			ba.Candidates = append(ba.Candidates, cand)
		}
	}
	ba.InvalidReasons = append(ba.InvalidReasons, detectBoundarySamples(scl, sda, samplePeriod)...)
	selectCandidate(ba.Candidates)
	return ba
}

func withClockPolarity(tr *trace, invert bool) *trace {
	if tr == nil || !invert {
		return tr
	}
	out := &trace{alias: tr.alias, inverted: !tr.inverted}
	for _, p := range tr.points {
		p.logical = !p.logical
		out.points = append(out.points, p)
	}
	return out
}

func detectI2CMissingEdges(falls, rises []point) []model.Diagnostic {
	var out []model.Diagnostic
	usedRise := map[int]bool{}
	for _, f := range falls {
		found := false
		for ri, r := range rises {
			if !usedRise[ri] && r.t > f.t {
				usedRise[ri] = true
				found = true
				break
			}
		}
		if !found {
			out = append(out, diagnostic("I2C_CLOCK_MISSING_EDGE", "error", "bus.i2c.scl", "falling SCL edge has no later rising edge; byte boundary cannot be uniquely closed", f.edge.ID))
		}
	}
	for ri, r := range rises {
		if !usedRise[ri] {
			out = append(out, diagnostic("I2C_CLOCK_MISSING_EDGE", "error", "bus.i2c.scl", "rising SCL edge has no preceding falling edge", r.edge.ID))
		}
	}
	return out
}

func groupClockSamples(rises []point, sampleRate, start, end float64) ([][]point, []string) {
	if len(rises) == 0 {
		return nil, nil
	}
	periods := []float64{}
	for i := 1; i < len(rises); i++ {
		periods = append(periods, rises[i].t-rises[i-1].t)
	}
	sort.Float64s(periods)
	median := 0.0
	if len(periods) > 0 {
		median = periods[len(periods)/2]
	}
	threshold := 0.00001
	if sampleRate > 0 {
		threshold = 5 / sampleRate
	}
	if median > 0 && median*10 > threshold {
		threshold = median * 10
	}
	groups := [][]point{{rises[0]}}
	var gapEdges []string
	for i := 1; i < len(rises); i++ {
		if rises[i].t-rises[i-1].t > threshold {
			gapEdges = append(gapEdges, rises[i-1].edge.ID, rises[i].edge.ID)
			groups = append(groups, nil)
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], rises[i])
	}
	return groups, uniqueStrings(gapEdges)
}

func detectClockStretch(falls, rises []point, sampleRate float64) []string {
	var ids []string
	periods := []float64{}
	for i := 1; i < len(falls); i++ {
		periods = append(periods, falls[i].t-falls[i-1].t)
	}
	sort.Float64s(periods)
	median := 0.0
	if len(periods) > 0 {
		median = periods[len(periods)/2]
	}
	threshold := 0.00002
	if sampleRate > 0 {
		threshold = 8 / sampleRate
	}
	if median > 0 && median*2 > threshold {
		threshold = median * 2
	}
	for _, f := range falls {
		for _, r := range rises {
			if r.t > f.t && r.t-f.t > threshold {
				ids = append(ids, f.edge.ID, r.edge.ID)
				break
			}
		}
	}
	return uniqueStrings(ids)
}

func detectBoundarySamples(clock, data *trace, samplePeriod float64) []model.Diagnostic {
	if samplePeriod <= 0 {
		return nil
	}
	var out []model.Diagnostic
	for _, dp := range data.changes() {
		for _, cp := range clock.changes() {
			d := dp.t - cp.t
			if d < 0 {
				d = -d
			}
			if d <= samplePeriod*1.5 {
				out = append(out, diagnostic("BOUNDARY_SAMPLE", "warning", "bus.i2c.boundary", "data changes within 1.5 sample periods of the clock sample edge", dp.edge.ID, cp.edge.ID))
			}
		}
	}
	return out
}

func decodeI2CByteGroup(bus model.BusConfig, sda *trace, group []point, groupIndex int, b boundary, candidateID, mode string) ([]model.Byte, string, string) {
	_ = bus
	var out []model.Byte
	for start := 0; start < len(group); start += 9 {
		endIndex := start + 9
		if endIndex > len(group) {
			endIndex = len(group)
		}
		samples := group[start:endIndex]
		var value uint8
		bits := ""
		complete := len(samples) == 9
		dataSamples := samples
		if complete {
			dataSamples = samples[:8]
		}
		for _, sp := range dataSamples {
			level, _ := sda.at(sp.t)
			if level {
				value |= 1 << uint(7-len(bits))
				bits += "1"
			} else {
				bits += "0"
			}
		}
		status := "decoded"
		reason := ""
		if !complete {
			status = "incomplete"
			reason = "I2C_CLOCK_MISSING_EDGE"
		}
		by := model.Byte{ID: stableID("i2c-byte", candidateID, fmt.Sprint(groupIndex), fmt.Sprint(start/9)),
			Index: groupIndex*100 + start/9, Status: status, StartTime: q(samples[0].t), EndTime: q(samples[len(samples)-1].t),
			SampleEdgeIDs: []string{samples[0].edge.ID, samples[len(samples)-1].edge.ID}, CandidateID: candidateID, InvalidReason: reason, Bits: bits}
		for _, sp := range samples {
			by.ClockEdgeIDs = append(by.ClockEdgeIDs, sp.edge.ID)
		}
		if complete {
			v := value
			by.Value = &v
			by.Hex = fmt.Sprintf("0x%02X", v)
			ackLevel, _ := sda.at(samples[8].t)
			by.AckEdgeID = samples[8].edge.ID
			_ = ackLevel
		}
		out = append(out, by)
	}
	status, reason := "decoded", ""
	if len(b.repeated) > 0 {
		status, reason = "repeated-start", "I2C_REPEATED_START"
	}
	if len(group) < 9 {
		status, reason = "incomplete", "I2C_CLOCK_MISSING_EDGE"
	}
	return out, status, reason
}

func ternary(v bool, a, b string) string {
	if v {
		return a
	}
	return b
}
func ternaryString(v bool, a, b string) string {
	if v {
		return a
	}
	return b
}

package decode

import (
	"fmt"

	"edgebenc/internal/domain"
)

// I2CDecoder reconstructs START/STOP segments and 9-bit (8 data + ACK) words.
// Two versions exist so a decoder-version revision is replayable:
//
//	v1.0.0 - samples SDA literally on every SCL rising edge.
//	v1.1.0 - ignores SDA pulses shorter than 0.25 SCL period (glitch filter).
type I2CDecoder struct{ ver string }

func (d I2CDecoder) Kind() string    { return "i2c" }
func (d I2CDecoder) Version() string { return d.ver }

func (d I2CDecoder) Decode(cap *domain.Capture, p *domain.ParamSet) ([]domain.Transaction, []domain.CandidateGroup, []domain.Diagnostic) {
	sigs, _ := BuildSignals(cap)
	return d.DecodeWithSignals(cap, p, sigs)
}

func (d I2CDecoder) DecodeWithSignals(cap *domain.Capture, p *domain.ParamSet, sigs map[string]*Signal) ([]domain.Transaction, []domain.CandidateGroup, []domain.Diagnostic) {
	var txs []domain.Transaction
	var groups []domain.CandidateGroup
	var diags []domain.Diagnostic
	scl, sda := sigs["scl"], sigs["sda"]
	if scl == nil || sda == nil {
		return nil, nil, []domain.Diagnostic{{Severity: "error", Code: "missing-signal",
			Where: "/channels", Message: "i2c requires scl and sda"}}
	}

	period := sclPeriod(scl)
	stretchThr := period * 3.0
	budget := p.MaxCandidates
	candidateCount := 0
	overBudget := false

	evs := scanI2CEvents(scl, sda, &diags, p, period, d.ver)

	type segment struct {
		startT float64
		endT   float64
		repeat bool
		open   bool // START without matching STOP (truncated)
	}
	var segs []segment
	inFrame := false
	startT := 0.0
	for _, ev := range evs {
		switch ev.what {
		case "start":
			if inFrame {
				segs = append(segs, segment{startT: startT, endT: ev.t, repeat: true})
			}
			startT = ev.t
			inFrame = true
		case "stop":
			if inFrame {
				segs = append(segs, segment{startT: startT, endT: ev.t})
				inFrame = false
			}
		}
	}
	truncIdx := -1
	if inFrame {
		segs = append(segs, segment{startT: startT, endT: evs[len(evs)-1].t, open: true})
		truncIdx = len(segs) - 1
	}

	for si, sg := range segs {
		tx := domain.Transaction{Index: si, Bus: "i2c", StartT: fmtT(sg.startT),
			EndT: fmtT(sg.endT), RepeatedStart: sg.repeat}
		if si == truncIdx {
			tx.Truncated = true
		}
		bits, stretches := collectI2CBits(scl, sda, sg.startT, sg.endT, stretchThr)
		tx.Stretched = stretches

		if p.GapThreshold.Normalized > 0 {
			if at, from := detectGap(bits, p.GapThreshold.Normalized); at >= 0 {
				tx.GapTerminated = true
				tx.GapFrom = fmtT(from)
				tx.EndT = fmtT(from)
				bits = bits[:at]
			}
		}

		bytes, tgroups, count, exhaust := groupI2CBits(bits, &tx, cap, p, budget-candidateCount)
		candidateCount += count
		overBudget = overBudget || exhaust
		groups = append(groups, tgroups...)
		tx.Bytes = bytes
		if tx.Truncated || tx.GapTerminated {
			tx.Flags = append(tx.Flags, "incomplete")
		}
		tx.ID = domain.StableID("tx", txKey(cap, p, "i2c", si, tx.StartT))
		txs = append(txs, tx)
	}

	if overBudget {
		groups = append(groups, domain.CandidateGroup{
			ID:     domain.StableID("grp", "budget-exhausted:"+cap.ID+":"+p.ID),
			Code:   "budget-exhausted",
			Reason: fmt.Sprintf("candidate enumeration capped at %d; remaining ambiguity not enumerated", budget),
		})
	}
	return txs, groups, diags
}

type i2cEvent struct {
	t    float64
	what string
}

func scanI2CEvents(scl, sda *Signal, diags *[]domain.Diagnostic, p *domain.ParamSet, period, ver string) []i2cEvent {
	times := mergeTimes(scl, sda)
	var evs []i2cEvent
	var prevSCL, prevSDA bool
	if len(times) > 0 {
		prevSCL, _ = scl.LevelAt(times[0] - period)
		prevSDA, _ = sda.LevelAt(times[0] - period)
	}
	boundaryTol := period * 0.12
	for _, t := range times {
		sclL, _ := scl.LevelAt(t)
		sdaL, _ := sda.LevelAt(t)

		// v1.1.0 glitch filter: SDA pulse shorter than 0.25 period while SCL high.
		isGlitch := false
		if ver == "dec-i2c/v1.1.0" && sclL {
			if _, d := sda.NearestEdge(t); d < period*0.25 {
				isGlitch = true
			}
		}

		if sclL && !prevSCL {
			evs = append(evs, i2cEvent{t: t, what: "rising"})
		}
		if !sclL && prevSCL {
			evs = append(evs, i2cEvent{t: t, what: "falling"})
		}
		if !isGlitch && sclL {
			if !sdaL && prevSDA {
				if _, d := sda.NearestEdge(t); d < boundaryTol && p.BoundaryMode == "strict" {
					*diags = append(*diags, domain.Diagnostic{Severity: "warning",
						Code: "boundary-sample", Where: "/t/" + fmtT(t),
						Message: "SDA transition within 12% of SCL edge at START; setup/hold ambiguous"})
				}
				evs = append(evs, i2cEvent{t: t, what: "start"})
			}
			if sdaL && !prevSDA {
				evs = append(evs, i2cEvent{t: t, what: "stop"})
			}
		}
		prevSCL, prevSDA = sclL, sdaL
	}
	return evs
}

func sclPeriod(scl *Signal) float64 {
	if len(scl.Times) < 2 {
		return 1e-6
	}
	var sum float64
	n := 0
	for i := 1; i < len(scl.Times); i++ {
		sum += scl.Times[i] - scl.Times[i-1]
		n++
	}
	return sum / float64(n)
}

type bitSample struct {
	t        float64
	value    bool
	edgeRefs []domain.EdgeRef
}

func collectI2CBits(scl, sda *Signal, startT, endT, stretchThr float64) ([]bitSample, []domain.StretchSpan) {
	var bits []bitSample
	var stretches []domain.StretchSpan
	lowStart := -1.0
	for i, t := range scl.Times {
		if t < startT || t > endT {
			continue
		}
		if !scl.Levels[i] {
			lowStart = t
			continue
		}
		// rising edge: sample SDA
		if lowStart >= 0 && t-lowStart > stretchThr {
			stretches = append(stretches, domain.StretchSpan{
				From: fmtT(lowStart), To: fmtT(t), Who: "slave", Us: (t - lowStart) * 1e6,
			})
		}
		sdaLevel, sdaIdx := sda.LevelAt(t)
		_, sclMatIdx := scl.LevelAt(t)
		bits = append(bits, bitSample{t: t, value: sdaLevel, edgeRefs: []domain.EdgeRef{
			roleRef(scl, sclMatIdx), roleRef(sda, sdaIdx),
		}})
		lowStart = -1
	}
	return bits, stretches
}

func detectGap(bits []bitSample, thr float64) (int, float64) {
	for i := 1; i < len(bits); i++ {
		if bits[i].t-bits[i-1].t > thr {
			return i, bits[i-1].t
		}
	}
	return -1, 0
}

func groupI2CBits(bits []bitSample, tx *domain.Transaction, cap *domain.Capture, p *domain.ParamSet, budget int) ([]domain.Byte, []domain.CandidateGroup, int, bool) {
	var out []domain.Byte
	var groups []domain.CandidateGroup
	candidateCount := 0
	overBudget := false
	byteIdx := 0
	for start := 0; start < len(bits); start += 9 {
		chunk := bits[start:]
		if len(chunk) > 9 {
			chunk = chunk[:9]
		}
		b := domain.Byte{Index: byteIdx, TStart: fmtT(chunk[0].t), TEnd: fmtT(chunk[len(chunk)-1].t)}
		for _, bs := range chunk {
			b.EdgeRefs = append(b.EdgeRefs, bs.edgeRefs...)
		}
		if byteIdx == 0 {
			b.Kind = "addr"
		} else {
			b.Kind = "data"
		}
		dataBits := chunk
		complete := len(chunk) == 9
		if len(chunk) > 8 {
			dataBits = chunk[:8]
		}
		// data value: known bits fixed; missing tail bits produce candidates.
		var fixed int
		for k := 0; k < len(dataBits) && k < 8; k++ {
			if dataBits[k].value {
				fixed |= 1 << uint(7-k)
			}
		}
		v := fixed
		b.Value = &v
		b.ValueText = fmt.Sprintf("0x%02X", v)
		if !complete {
			knownData := len(dataBits)
			unknown := 8 - knownData
			if unknown < 0 {
				unknown = 0
			}
			b.ValueText = fmt.Sprintf("0x%02X*", fixed)
			b.Flags = append(b.Flags, "missing-clock-edge")
			gid := domain.StableID("grp", struct {
				Tx   string
				Byte int
				Code string
			}{tx.ID, byteIdx, "missing-clock-edge"})
			items, used, exhaust := enumTailCandidates(fixed, knownData, unknown, p.MaxCandidates, budget)
			candidateCount += used
			overBudget = overBudget || exhaust
			reason := fmt.Sprintf("%d SCL samples present; %d clock edge(s) missing before fragment ended", len(chunk), 9-len(chunk))
			if len(chunk) == 8 {
				reason = "8 data bits present but ACK clock edge missing; ACK undetermined"
			}
			groups = append(groups, domain.CandidateGroup{
				ID: gid, Code: "missing-clock-edge", Reason: reason, Items: items,
			})
		}
		// ACK/NACK is the 9th sample.
		if complete {
			if chunk[8].value {
				b.Ack = "nack"
			} else {
				b.Ack = "ack"
			}
		} else if len(chunk) > 8 {
			b.Ack = ""
		}
		b.ID = domain.StableID("byte", struct {
			Tx   string
			Byte int
		}{tx.ID, byteIdx})
		out = append(out, b)
		byteIdx++
	}
	return out, groups, candidateCount, overBudget
}

// enumTailCandidates enumerates every value consistent with known MSBs;
// unobserved tail bits vary. It never exceeds p.MaxCandidates or budget.
func enumTailCandidates(fixed, known, unknown, maxCand, budget int) ([]domain.Candidate, int, bool) {
	total := 1
	if unknown > 0 {
		total = 1 << uint(unknown)
	}
	limit := total
	if maxCand > 0 && limit > maxCand {
		limit = maxCand
	}
	if budget < limit {
		limit = budget
	}
	exhaust := limit < total
	conf := 1.0
	if total > 1 {
		conf = 1.0 / float64(total)
	}
	items := make([]domain.Candidate, 0, limit)
	for v := 0; v < limit; v++ {
		val := fixed | v
		items = append(items, domain.Candidate{
			ID:         domain.StableID("cand", struct{ Fixed, V int }{fixed, v}),
			Values:     []string{fmt.Sprintf("0x%02X", val)},
			Confidence: conf,
			Reason:     fmt.Sprintf("%d known MSB(s), %d unobserved tail bit(s)", known, unknown),
		})
	}
	return items, limit, exhaust
}

func txKey(cap *domain.Capture, p *domain.ParamSet, bus string, idx int, t0 string) any {
	return struct {
		Cap, Param, Bus string
		Idx             int
		T0              string
	}{cap.ID, p.ID, bus, idx, t0}
}

func init() {
	register(I2CDecoder{ver: "dec-i2c/v1.0.0"})
	register(I2CDecoder{ver: "dec-i2c/v1.1.0"})
}

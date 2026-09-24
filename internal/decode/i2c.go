package decode

import (
	"fmt"
	"sort"

	"edgedesk/internal/model"
)

type busEvent struct {
	sample int64
	line   int // 0=clock 1=data
	level  int
}

// decodeI2C decodes I2C from SCL/SDA edges. Repeated START, clock stretching
// and data gaps are structured states; missing clock edges retain an
// alternative-alignment candidate.
func decodeI2C(cap *model.Capture, p *model.Params, r *model.Run) {
	scl := buildLine(cap, p.ClockChannel)
	sda := buildLine(cap, p.DataChannel)

	if len(scl.edges) == 0 {
		r.Status = model.StatusSemanticUnsolv
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "error", Code: model.DiagSemanticUnsolv,
			Position: p.ClockChannel, Message: "no clock edges on SCL; cannot hypothesize"})
		return
	}

	var tl []busEvent
	for _, e := range scl.edges {
		tl = append(tl, busEvent{e.Sample, 0, e.Level})
	}
	for _, e := range sda.edges {
		tl = append(tl, busEvent{e.Sample, 1, e.Level})
	}
	sort.SliceStable(tl, func(i, j int) bool {
		if tl[i].sample != tl[j].sample {
			return tl[i].sample < tl[j].sample
		}
		return tl[i].line < tl[j].line
	})

	primary := i2cHypothesis(cap, p, tl, "rising")
	conf := 1.0
	for _, d := range primary.diags {
		switch d.Code {
		case model.DiagMissingClock:
			conf -= 0.4
		case model.DiagDataGap, model.DiagRepeatedStart, model.DiagClockStretch:
			conf -= 0.05
		}
	}
	if conf < 0.05 {
		conf = 0.05
	}
	r.Candidates = append(r.Candidates, model.Candidate{
		Group: "i2c:" + cap.ID, Hypothesis: "i2c-standard", Confidence: conf,
		Reasons:      []string{"sda sampled on scl rising edge"},
		Transactions: primary.txs, Diagnostics: primary.diags,
	})

	if primary.missingClock {
		alt := i2cHypothesis(cap, p, tl, "falling")
		r.Candidates = append(r.Candidates, model.Candidate{
			Group: "i2c:" + cap.ID, Hypothesis: "i2c-alt-alignment", Confidence: 0.4,
			Reasons:      []string{"missing_clock_edges: sda sampled on scl falling edge as alternative"},
			Transactions: alt.txs, Diagnostics: alt.diags,
		})
		r.Diagnostics = append(r.Diagnostics, model.Diag{
			Severity: "warning", Code: model.DiagMissingClock, Position: p.ClockChannel,
			Message: "clock edge gaps detected; multiple decode candidates retained"})
	}
}

type i2cResult struct {
	txs          []model.Transaction
	diags        []model.Diag
	missingClock bool
}

type bitSample struct {
	bit   int
	clock model.EdgeRef
}

func i2cHypothesis(cap *model.Capture, p *model.Params, tl []busEvent, sampleOn string) i2cResult {
	res := i2cResult{}
	sclLevel, sdaLevel := 1, 1 // i2c idle high
	inTx := false
	var bits []bitSample
	var txStart int64
	var flags []string
	lastClockFall := int64(-1)
	lastEventSample := int64(-1)
	sdaTogglesWhileLow := 0

	closeTx := func(endSample int64, truncated bool) {
		if !inTx {
			return
		}
		if truncated {
			flags = append(flags, "truncated-gap")
		}
		if len(bits) > 0 {
			tx := packI2C(bits, txStart, endSample, flags, p)
			res.txs = append(res.txs, tx)
		}
		bits = nil
		flags = nil
		inTx = false
	}

	for _, ev := range tl {
		// Data gap: idle longer than threshold closes the transaction; the
		// bytes after the gap are never spliced into it.
		if inTx && p.GapSamples > 0 && lastEventSample >= 0 && ev.sample-lastEventSample > p.GapSamples {
			closeTx(lastEventSample, true)
			res.diags = append(res.diags, model.Diag{
				Severity: "warning", Code: model.DiagDataGap,
				Position: fmt.Sprintf("sample@%d", lastEventSample),
				Message:  "data gap; transaction closed, bytes after gap not spliced"})
		}
		lastEventSample = ev.sample

		if ev.line == 0 { // SCL
			if ev.level == 1 { // rising
				if p.StretchSamples > 0 && lastClockFall >= 0 && ev.sample-lastClockFall > p.StretchSamples && inTx {
					if !hasFlag(flags, "clock-stretch") {
						flags = append(flags, "clock-stretch")
					}
					res.diags = append(res.diags, model.Diag{
						Severity: "warning", Code: model.DiagClockStretch,
						Position: fmt.Sprintf("sample@%d", ev.sample),
						Message:  "clock stretching: SCL held low beyond threshold"})
				}
				if inTx && sampleOn == "rising" {
					bits = append(bits, bitSample{sdaLevel, model.EdgeRef{ChannelID: p.ClockChannel, Sample: ev.sample}})
				}
				if sclLevel == 0 {
					sdaTogglesWhileLow = 0
				}
				sclLevel = 1
			} else { // falling
				lastClockFall = ev.sample
				if inTx && sampleOn == "falling" {
					bits = append(bits, bitSample{sdaLevel, model.EdgeRef{ChannelID: p.ClockChannel, Sample: ev.sample}})
				}
				sclLevel = 0
			}
		} else { // SDA
			if ev.level == 0 && sclLevel == 1 { // START
				if inTx { // repeated START
					// the SCL rising edge before a repeated START is the
					// setup phase, not a data bit: trim to 9-bit boundary
					if rem := len(bits) % 9; rem != 0 {
						bits = bits[:len(bits)-rem]
					}
					closeTx(ev.sample, false)
					inTx = true
					txStart = ev.sample
					flags = append(flags, "repeated-start")
					res.diags = append(res.diags, model.Diag{
						Severity: "warning", Code: model.DiagRepeatedStart,
						Position: fmt.Sprintf("sample@%d", ev.sample),
						Message:  "repeated START; new transaction segment opened"})
				} else {
					inTx = true
					txStart = ev.sample
				}
			} else if ev.level == 1 && sclLevel == 1 { // STOP
				// the SCL rising edge before STOP is not a data bit
				if rem := len(bits) % 9; rem != 0 {
					bits = bits[:len(bits)-rem]
				}
				closeTx(ev.sample, false)
			} else {
				if sclLevel == 0 && inTx {
					sdaTogglesWhileLow++
					if sdaTogglesWhileLow >= 2 {
						res.missingClock = true
					}
				}
			}
			sdaLevel = ev.level
		}
	}
	closeTx(lastEventSample, false)
	return res
}

func packI2C(bits []bitSample, start, end int64, flags []string, p *model.Params) model.Transaction {
	var bytes []model.Byte
	for i := 0; i+8 <= len(bits); i += 9 {
		val := 0
		var refs []model.EdgeRef
		for k := 0; k < 8 && i+k < len(bits); k++ {
			val = val<<1 | bits[i+k].bit
			refs = append(refs, bits[i+k].clock)
		}
		var ack *bool
		if i+8 < len(bits) {
			ack = boolPtr(bits[i+8].bit == 0)
			refs = append(refs, bits[i+8].clock)
		}
		bytes = append(bytes, NewByte(val, ack, refs, 8))
	}
	kind := "i2c-write"
	addr := 0
	if len(bytes) > 0 {
		addr = bytes[0].Value >> 1
		if bytes[0].Value&1 == 1 {
			kind = "i2c-read"
		}
	}
	return NewTransaction(kind, "", addr, bytes, start, end, flags)
}

func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

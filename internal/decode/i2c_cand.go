package decode

import (
	"fmt"
	"math/big"

	"edgebench/internal/domain"
)

// mkCandidatesRepeated keeps the alternative legal framing of a repeated
// START: one combined transfer (adopted default) vs per-leg transfers. Both
// are recorded with evidence-linked time spans.
func mkCandidatesRepeated(res *domain.RunResult, runID, tid string, marks []mark, s seg,
	by []*byteRead, window, gapLimit, stretchFactor *big.Rat, scl, sda *chTimeline, maxBytes int) {
	_ = big.NewRat

	target := tid
	var reps []int
	for k := s.start + 1; k < len(marks) && (s.stop < 0 || k < s.stop); k++ {
		if marks[k].kind == "rep" {
			reps = append(reps, k)
		}
	}
	if len(reps) == 0 {
		return
	}
	// Combined framing candidate (the transfer already in res.Txs).
	combined := hexSummary(by)
	c1 := domain.Candidate{ID: candID(runID, target, domain.DiagRepeated+"-combined", combined),
		RunID: runID, TargetID: target, Reason: domain.DiagRepeated, Confidence: 0.9,
		ScoreParts: []string{"single START..STOP transfer=0.7", "address leg consistency=0.2"},
		Values: combined, Adopted: true,
		EdgeStart: marks[s.start].idx, EdgeEnd: marks[s.stop].idx,
		StartPs: marks[s.start].t.RatString(), EndPs: marks[s.stop].t.RatString()}
	// Per-leg framing candidate values.
	bounds := append([]int{s.start}, reps...)
	if s.stop >= 0 {
		bounds = append(bounds, s.stop)
	}
	var legs []string
	for k := 0; k+1 < len(bounds); k++ {
		t0, t1 := marks[bounds[k]].t, marks[bounds[k+1]].t
		brs, _ := readI2CBits(scl, sda, t0, t1, window, gapLimit, stretchFactor, maxBytes)
		legs = append(legs, hexSummary(groupBytes(brs))...)
	}
	c2 := domain.Candidate{ID: candID(runID, target, domain.DiagRepeated+"-legs", legs),
		RunID: runID, TargetID: target, AltID: "alt-" + stableID(runID, tid, "legs"),
		Reason: domain.DiagRepeated, Confidence: 0.6,
		ScoreParts: []string{fmt.Sprintf("%d legal legs=0.4", len(legs)), "no STOP between legs=-0.1"},
		Values: legs,
		EdgeStart: marks[s.start].idx, EdgeEnd: marks[s.stop].idx,
		StartPs: marks[s.start].t.RatString(), EndPs: marks[s.stop].t.RatString()}
	res.Candidates = append(res.Candidates, c1, c2)
	res.Diags = append(res.Diags, domain.Diag{Kind: domain.DiagRepeated, Severity: "info",
		Edge: marks[reps[0]].sda.Index, AtText: marks[reps[0]].t.RatString(),
		Detail: fmt.Sprintf("repeated START splits transfer into %d legal legs; both framings retained", len(legs))})
}

// mkByteCandidates enumerates deterministic alternatives for bytes whose bit
// is ambiguous at the sample window, and records unresolved missing-edge
// states without inventing completions.
func mkByteCandidates(res *domain.RunResult, runID, tid string, by []*byteRead) {
	for _, b := range by {
		if b.missing == domain.DiagGap {
			continue
		}
		if b.boundary && len(b.bits) >= 8 {
			xpos := []int{}
			for i := 0; i < 8; i++ {
				if b.bits[i] == 'x' {
					xpos = append(xpos, i)
				}
			}
			values := boundaryValues(b.bits[:8])
			c := domain.Candidate{ID: candID(runID, byteID(runID, tid, indexOfByte(res, b, tid), b.bits, b.missing), domain.DiagBoundary, values),
				RunID: runID, TargetID: byteID(runID, tid, indexOfByte(res, b, tid), b.bits, b.missing),
				Reason: domain.DiagBoundary, Confidence: 1.0 / float64(len(values)),
				ScoreParts: []string{fmt.Sprintf("%d bits at SCL sample boundary", len(xpos)), fmt.Sprintf("%d equally legal values", len(values))},
				Values: values, EdgeStart: b.sEdge.Index, EdgeEnd: b.eEdge.Index,
				StartPs: ratStr(b.start), EndPs: ratStr(b.end)}
			res.Candidates = append(res.Candidates, c)
			res.Diags = append(res.Diags, domain.Diag{Kind: domain.DiagBoundary, Severity: "warning",
				Edge: b.eEdge.Index, AtText: ratStr(b.end),
				Detail: fmt.Sprintf("byte has %d boundary bits; %d values retained: %v", len(xpos), len(values), values)})
		}
		if b.missing == domain.DiagMissingEdge {
			c := domain.Candidate{ID: candID(runID, tid, domain.DiagMissingEdge, []string{b.bits}),
				RunID: runID, TargetID: tid, Reason: domain.DiagMissingEdge, Confidence: 0,
				ScoreParts: []string{"incomplete clock cycles", "no legal completion without more edges"},
				Values: []string{}, EdgeStart: b.sEdge.Index, EdgeEnd: b.eEdge.Index,
				StartPs: ratStr(b.start), EndPs: ratStr(b.end)}
			res.Candidates = append(res.Candidates, c)
		}
	}
}

func indexOfByte(res *domain.RunResult, b *byteRead, tid string) int {
	for i := range res.Txs {
		if res.Txs[i].ID == tid {
			return len(res.Txs[i].Bytes) - 1
		}
	}
	return 0
}

func boundaryValues(bits8 string) []string {
	x := []int{}
	for i, c := range bits8 {
		if c == 'x' {
			x = append(x, i)
		}
	}
	if len(x) > 8 {
		x = x[:8]
	}
	n := 1 << uint(len(x))
	out := make([]string, 0, n)
	for v := 0; v < n; v++ {
		bs := []byte(bits8)
		for j, pos := range x {
			if (v>>uint(len(x)-1-j))&1 == 1 {
				bs[pos] = '1'
			} else {
				bs[pos] = '0'
			}
		}
		val := 0
		for _, c := range bs {
			val = val<<1 | int(c-'0')
		}
		out = append(out, fmt.Sprintf("%02X", val))
	}
	return out
}

func hexSummary(by []*byteRead) []string {
	var out []string
	for _, b := range by {
		if b.missing == domain.DiagGap {
			out = append(out, "|")
			continue
		}
		if len(b.bits) >= 8 {
			s := fmt.Sprintf("%02X", b.value)
			if len(b.bits) == 9 {
				if b.bits[8] == '0' {
					s += "+A"
				} else if b.bits[8] == '1' {
					s += "+N"
				} else {
					s += "+?"
				}
			}
			out = append(out, s)
		} else if b.missing == domain.DiagMissingEdge {
			out = append(out, "["+b.bits+"]")
		}
	}
	return out
}

var _ = big.NewRat

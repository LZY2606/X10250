// Package decode turns immutable captures into reproducible, multi-candidate
// decodings. It is pure Go: no browser logic, directly callable from tests.
package decode

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"edgebench/internal/model"
)

// MaxCandidates caps alternative enumeration so ambiguity cannot exhaust
// resources; hitting the cap is a distinct resource_exhausted result.
const MaxCandidates = 16

// InputHash summarizes exactly the facts and parameters this run consumed.
func InputHash(cap model.Capture, p model.Params) string {
	b, _ := json.Marshal(struct {
		Edges      []model.Edge        `json:"edges"`
		Aliases    []model.ChAlias     `json:"aliases"`
		Polarities []model.PolaritySrc `json:"polarities"`
		SampleRate model.Orig          `json:"sample_rate"`
		Kind       string              `json:"kind"`
		Params     model.Params        `json:"params"`
	}{cap.Edges, cap.Aliases, cap.Polarities, cap.SampleRate, cap.Kind, p})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

// bit is one observed (or missing) clocked bit.
type bit struct {
	order    int
	val      *bool // nil => ambiguous between options
	edgeSeq  int
	sampleAt float64
	basis    *model.BasisEvent
}

type rawByte struct {
	pos       int
	dataBits  []*bit // up to 8; missing positions appended as ambiguous
	ack       *bit
	startSec  float64
	endSec    float64
	edgeSeqs  []int
	sampleAts []float64
}

type rawFrame struct {
	capID      string
	protocol   string
	device     string
	frameSeq   int
	startAt    float64
	endAt      float64
	startSeqs  []int
	bytes      []rawByte
	basis      []model.BasisEvent
	terminated string
	note       string
}

type scannerOut struct {
	frames      []rawFrame
	diags       []model.Diagnostic
	noClockLine bool
}

// Decode runs the decoder for one capture under one parameter set. It is
// deterministic: identical inputs always produce identical ordering.
func Decode(cap model.Capture, p model.Params, parentEvent string) model.DecodeResult {
	res := model.DecodeResult{
		CaptureID:   cap.ID,
		ParamsID:    p.ID,
		DecoderVer:  model.DecoderVersion,
		InputHash:   InputHash(cap, p),
		ParentEvent: parentEvent,
		CreatedAt:   time.Now().UTC(),
	}
	res.ID = ResultID(cap.ID, p.ID, res.InputHash, res.DecoderVersion)

	tl, diags := buildTimelines(cap)
	res.Diagnostics = append(res.Diagnostics, diags...)

	var out scannerOut
	switch cap.Kind {
	case "i2c":
		out = scanI2C(tl, p)
	case "spi", "custom":
		out = scanSync(cap.Kind, tl, p)
	default:
		out = scannerOut{noClockLine: true}
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: "UNKNOWN_BUS_KIND", Severity: "error",
			Message: fmt.Sprintf("bus kind %q is not supported", cap.Kind),
		})
	}
	res.Diagnostics = append(res.Diagnostics, out.diags...)
	if out.noClockLine {
		res.Status = model.StatusSemanticUnsolvable
		res.Candidates = nil
		return res
	}
	if len(res.Diagnostics) > 0 {
		for _, d := range res.Diagnostics {
			if d.Severity == "error" && len(out.frames) == 0 {
				res.Status = model.StatusIncomplete
				return res
			}
		}
	}

	exhausted := false
	for fi, rf := range out.frames {
		rf.capID = cap.ID
		rf.frameSeq = fi + 1
		cands, rem := enumerate(res.ID, fi, rf, p)
		if rem > 0 {
			exhausted = true
			seq := -1
			if len(rf.startSeqs) > 0 {
				seq = rf.startSeqs[0]
			}
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: "CANDIDATE_BUDGET", Severity: "warning", EdgeSeq: intPtr(seq),
				Message: fmt.Sprintf("frame %d has more than %d legal decodings; %d alternatives not enumerated",
					fi+1, MaxCandidates, rem),
			})
		}
		res.Candidates = append(res.Candidates, cands...)
	}

	switch {
	case exhausted:
		res.Status = model.StatusResourceExhausted
	case len(res.Diagnostics) > 0 && hasError(res.Diagnostics):
		res.Status = model.StatusIncomplete
	default:
		res.Status = model.StatusDecoded
	}
	return res
}

func intPtr(v int) *int {
	if v < 0 {
		return nil
	}
	return &v
}

func hasError(ds []model.Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

func enumerate(resultID string, idx int, rf rawFrame, p model.Params) ([]model.FrameCandidate, int) {
	type amb struct {
		b  *rawByte
		i  int
		bt *bit
	}
	var ambits []amb
	values := [][]bool{}
	for bi := range rf.bytes {
		rb := &rf.bytes[bi]
		for len(rb.dataBits) < 8 {
			miss := &bit{order: len(rb.dataBits), basis: &model.BasisEvent{
				Code:    model.BasisMissingClockEdge,
				Message: "clock edge never arrived for this bit position",
			}}
			rb.dataBits = append(rb.dataBits, miss)
		}
		for i := range rb.dataBits {
			bt := rb.dataBits[i]
			if bt.val == nil {
				ambits = append(ambits, amb{rb, i, bt})
				values = append(values, []bool{false, true})
			}
		}
		if rb.ack != nil && rb.ack.val == nil {
			ambits = append(ambits, amb{rb, 8, rb.ack})
			values = append(values, []bool{false, true})
		}
	}

	frameKey := fmt.Sprintf("%s:%.9f", rf.protocol, rf.startAt)
	altGroup := "alt_" + hash12(resultID, frameKey, idx)

	combos := combos(values)
	total := len(combos)
	rem := 0
	if total > MaxCandidates {
		rem = total - MaxCandidates
		combos = combos[:MaxCandidates]
	}
	if total == 0 {
		combos = [][]bool{nil}
	}

	var cands []model.FrameCandidate
	seen := map[string]bool{}
	for ci, combo := range combos {
		for k, a := range ambits {
			v := combo[k]
			if a.i == 8 {
				a.b.ack.val = &v
			} else {
				a.b.dataBits[a.i].val = &v
			}
		}
		fc := model.FrameCandidate{
			FrameSeq:  rf.frameSeq,
			AltGroup:  altGroup,
			Protocol:  rf.protocol,
			Device:    rf.device,
			StartSec:  rf.startAt,
			EndSec:    rf.endAt,
			Basis:     append([]model.BasisEvent{}, rf.basis...),
			ValidFrom: rf.startAt,
			ValidTo:   rf.endAt,
			Note:      rf.note,
		}
		keyParts := []any{frameKey}
		for bi := range rf.bytes {
			bv := toByteValue(rf, &rf.bytes[bi], p, frameKey)
			fc.Bytes = append(fc.Bytes, bv)
			keyParts = append(keyParts, bv.Hex, bv.ACKKnown, bv.ACK)
		}
		ambCodes := map[string]int{}
		for _, b := range fc.Basis {
			ambCodes[b.Code]++
		}
		for _, a := range ambits {
			bt := a.bt
			if bt.basis != nil {
				ambCodes[bt.basis.Code]++
				fc.Basis = appendUniqueBasis(fc.Basis, *bt.basis)
			}
		}
		fc.Confidence = confidence(fc.Basis, len(ambits))
		key := hash12(keyParts...)
		if seen[key] {
			continue
		}
		seen[key] = true
		fc.ID = CandidateID(resultID, altGroup, append(keyParts, ci))
		cands = append(cands, fc)
	}
	return cands, rem
}

func appendUniqueBasis(in []model.BasisEvent, b model.BasisEvent) []model.BasisEvent {
	for _, x := range in {
		if x.Code == b.Code && x.At == b.At {
			return in
		}
	}
	return append(in, b)
}

func combos(opts [][]bool) [][]bool {
	out := [][]bool{{}}
	for _, o := range opts {
		var next [][]bool
		for _, pre := range out {
			for _, v := range o {
				row := make([]bool, 0, len(pre)+1)
				row = append(row, pre...)
				row = append(row, v)
				next = append(next, row)
			}
		}
		out = next
	}
	return out
}

func confidence(basis []model.BasisEvent, nAmb int) float64 {
	c := 1.0
	weight := map[string]float64{
		model.BasisMissingClockEdge: 0.3,
		model.BasisBoundarySample:   0.25,
		model.BasisSPIModeAmbiguous: 0.2,
		model.BasisClockStretch:     0.1,
		model.BasisDataGap:          0.1,
		model.BasisRepeatedStart:    0.05,
		model.BasisMultiCS:          0.1,
	}
	counts := map[string]int{}
	for _, b := range basis {
		counts[b.Code]++
	}
	for code, n := range counts {
		if w, ok := weight[code]; ok {
			c -= w * float64(n)
		}
	}
	c -= 0.05 * float64(nAmb)
	if c < 0.2 {
		c = 0.2
	}
	return float64(int(c*100+0.5)) / 100
}

func toByteValue(rf rawFrame, rb *rawByte, p model.Params, frameKey string) model.ByteValue {
	v := 0
	seqs := append([]int{}, rb.edgeSeqs...)
	for i, bt := range rb.dataBits {
		if bt.val != nil && *bt.val {
			shift := uint(7 - i)
			if p.BitOrder == "lsb-first" {
				shift = uint(i)
			}
			v |= 1 << shift
		}
		if bt.edgeSeq != 0 {
			seqs = append(seqs, bt.edgeSeq)
		}
	}
	bv := model.ByteValue{
		SlotID:   SlotID(rf.capID, rf.protocol, rf.device, frameKey, rb.pos),
		Order:    rb.pos,
		Value:    v,
		Hex:      fmt.Sprintf("%02X", v),
		StartSec: rb.startSec,
		EndSec:   rb.endSec,
		EdgeSeqs: dedupInts(seqs),
		SampleAt: append([]float64{}, rb.sampleAts...),
	}
	if rb.ack != nil {
		bv.ACKKnown = true
		ack := rb.ack.val != nil && !*rb.ack.val
		bv.ACK = &ack
	}
	return b
}

func dedupInts(in []int) []int {
	seen := map[int]bool{}
	var out []int
	for _, v := range in {
		if v != 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

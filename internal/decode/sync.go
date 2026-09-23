package decode

import (
	"fmt"

	"edgedesk/internal/model"
)

// decodeSync decodes a custom synchronous channel pair (clock + data)
// into fixed-size words. Unknown clock polarity keeps both polarities as
// candidates; gaps split words into separate transactions, never stitched.
func decodeSync(data, clk []nedge, polarity string, p model.ParamSet) ([]Candidate, []model.Diagnostic, []model.Judgment) {
	var diags []model.Diagnostic
	var judges []model.Judgment
	if len(clk) == 0 || len(data) == 0 {
		return nil, nil, nil
	}
	pols := []string{polarity}
	if polarity == "" {
		pols = []string{"rising", "falling"}
		judges = append(judges, model.Judgment{
			Rule: "clock_polarity_unknown", Action: "keep_multiple_candidates",
			Subject: "sync",
			Detail:  map[string]string{"polarities": "rising,falling"},
		})
	}
	var out []Candidate
	for _, pol := range pols {
		want := 1
		if pol == "falling" {
			want = 0
		}
		// split clock edges into gap-delimited segments
		type seg struct{ bits []bitCell }
		var segs []seg
		var cur []bitCell
		prev := int64(-1)
		for _, e := range clk {
			if prev >= 0 && e.t-prev > p.GapThresholdPs && len(cur) > 0 {
				segs = append(segs, seg{bits: cur})
				cur = nil
				diags = append(diags, model.Diagnostic{
					Code: "data_gap", EdgeID: e.id, Tps: e.t, HasPos: true,
					Message: fmt.Sprintf("data gap of %s; sync words not stitched across gap", model.FormatPs(e.t-prev)),
				})
				judges = append(judges, model.Judgment{
					Rule: "data_gap", Action: "close_transaction_no_stitch",
					Subject: "sync", Detail: map[string]string{"at_ps": fmt.Sprint(e.t)},
				})
			}
			prev = e.t
			if e.level != want {
				continue
			}
			lvl := 1 - data[0].level
			for _, de := range data {
				if de.t > e.t {
					break
				}
				lvl = de.level
			}
			cur = append(cur, bitCell{
				v: lvl, amb: ambNear(data, e.t, p.I2CBoundaryWindowPs),
				t: e.t, edgeID: e.id,
			})
		}
		if len(cur) > 0 {
			segs = append(segs, seg{bits: cur})
		}
		nAmb := 0
		for _, s := range segs {
			for _, b := range s.bits {
				if b.amb {
					nAmb++
				}
			}
		}
		assigns := expandAmb(nAmb, p.MaxCandidates)
		for _, assign := range assigns {
			cand := Candidate{Bus: "sync", Note: "polarity=" + pol, Confidence: 1.0}
			if polarity == "" {
				cand.Confidence = 0.5
			}
			ai := 0
			for _, s := range segs {
				txn := Txn{
					Kind: "sync", Device: "sync",
					StartPs: s.bits[0].t, EndPs: s.bits[len(s.bits)-1].t,
					Meta: map[string]string{"polarity": pol, "bits_per_word": fmt.Sprint(p.BitsPerWord)},
				}
				w := p.BitsPerWord
				if w <= 0 {
					w = 8
				}
				nfull := len(s.bits) / w
				for c := 0; c < nfull; c++ {
					bits := s.bits[c*w : c*w+w]
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
						Value: val, HasValue: true, Valid: true,
						FirstEdgeID: bits[0].edgeID, LastEdgeID: bits[w-1].edgeID,
					})
				}
				if rem := len(s.bits) - nfull*w; rem > 0 {
					bits := s.bits[nfull*w:]
					txn.Bytes = append(txn.Bytes, Byte{
						HasValue: false, Valid: false,
						InvalidReason: fmt.Sprintf("partial word: %d of %d bits", rem, w),
						FirstEdgeID:   bits[0].edgeID, LastEdgeID: bits[len(bits)-1].edgeID,
					})
				}
				if len(txn.Bytes) > 0 {
					cand.Txns = append(cand.Txns, txn)
				}
			}
			if len(cand.Txns) == 0 {
				continue
			}
			for i := 0; i < nAmb; i++ {
				cand.Confidence *= 0.85
			}
			out = append(out, cand)
		}
	}
	return out, diags, judges
}

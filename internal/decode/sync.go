package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// DecodeSync decodes a custom synchronous channel pair (dclk/dat) into
// fixed-size words sampled on the configured clock polarity.
func DecodeSync(p *Prepared, cfg *model.ProbeConfig) ([]*model.Candidate, []model.Diagnostic, *model.Diagnostic) {
	dclk, ok1 := p.ByRole["dclk"]
	dat, ok2 := p.ByRole["dat"]
	if !ok1 || !ok2 {
		missing := "dclk"
		if !ok2 {
			missing = "dat"
		}
		return nil, nil, &model.Diagnostic{Code: model.CodeMissingChannel, Severity: "error",
			Message: fmt.Sprintf("sync decode needs channel with role %q", missing), Channel: missing}
	}
	word := cfg.WordSize
	if word <= 0 {
		word = 8
	}
	wantRising := cfg.ClockPolarity != "falling"
	cand := &model.Candidate{Protocol: "sync", Confidence: 0.9,
		Reasons: []string{"polarity_" + cfg.ClockPolarity}}
	var bits []int
	var bitT []int64
	var bitRef []int
	for i, t := range p.Times[dclk] {
		rising := p.Levels[dclk][i] == 1
		if rising != wantRising {
			continue
		}
		bits = append(bits, p.levelAt(dat, t, 0))
		bitT = append(bitT, t)
		bitRef = append(bitRef, p.EdgeRefs[dclk][i])
	}
	if len(bits) < word {
		return nil, nil, &model.Diagnostic{Code: model.CodeUnsolvable, Severity: "error",
			Message: fmt.Sprintf("sync: %d bits available, need at least one %d-bit word", len(bits), word),
			Channel: dclk}
	}
	tx := model.Transaction{Device: "sync0", StartNs: bitT[0], EndNs: bitT[len(bitT)-1]}
	for b := 0; b+word <= len(bits); b += word {
		v := 0
		for k := 0; k < word; k++ {
			v = v<<1 | bits[b+k]
		}
		tx.Bytes = append(tx.Bytes, model.Byte{
			Value: v, StartNs: bitT[b], EndNs: bitT[b+word-1],
			EdgeIdx0: bitRef[b], EdgeIdx1: bitRef[b+word-1],
		})
	}
	cand.Txs = append(cand.Txs, tx)
	return []*model.Candidate{cand}, nil, nil
}

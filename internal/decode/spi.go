package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// DecodeSPI decodes SCLK/MOSI framed by one or more chip-select channels.
// When the configured mode is "auto" every legal mode is kept as a separate
// candidate; a data edge simultaneous with the sampling clock edge (boundary
// sampling) yields an additional candidate with the alternative bit reading.
func DecodeSPI(p *Prepared, cfg *model.ProbeConfig) ([]*model.Candidate, []model.Diagnostic, *model.Diagnostic) {
	sclk, ok1 := p.ByRole["sclk"]
	mosi, ok2 := p.ByRole["mosi"]
	csList := p.ByRoleAll["cs"]
	if !ok1 || !ok2 || len(csList) == 0 {
		missing := "sclk"
		if !ok2 {
			missing = "mosi"
		} else if len(csList) == 0 {
			missing = "cs"
		}
		return nil, nil, &model.Diagnostic{Code: model.CodeMissingChannel, Severity: "error",
			Message: fmt.Sprintf("spi decode needs channel with role %q", missing), Channel: missing}
	}
	var diags []model.Diagnostic
	if len(csList) > 1 {
		diags = append(diags, model.Diagnostic{Code: model.CodeMultiDeviceCS, Severity: "info",
			Message: fmt.Sprintf("%d chip-select channels: transactions grouped per device", len(csList))})
	}
	modes := []int{}
	if cfg.SPIMode == "auto" || cfg.SPIMode == "" {
		modes = []int{0, 1, 2, 3}
	} else {
		var m int
		if _, err := fmt.Sscanf(cfg.SPIMode, "%d", &m); err != nil || m < 0 || m > 3 {
			return nil, nil, &model.Diagnostic{Code: model.CodeUnsolvable, Severity: "error",
				Message: fmt.Sprintf("spi_mode %q is not auto|0|1|2|3", cfg.SPIMode)}
		}
		modes = []int{m}
	}
	// frames per cs channel: [assertNs, deassertNs)
	type frame struct {
		dev        string
		start, end int64
	}
	var frames []frame
	for _, cs := range csList {
		ts := p.Times[cs]
		ls := p.Levels[cs]
		for i := 0; i < len(ts); i++ {
			if ls[i] == 0 { // assert (active low)
				end := int64(1<<62 - 1)
				if i+1 < len(ts) {
					end = ts[i+1]
				}
				frames = append(frames, frame{dev: cs, start: ts[i], end: end})
			}
		}
	}
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].start < frames[j].start })
	if len(frames) == 0 {
		return nil, diags, &model.Diagnostic{Code: model.CodeUnsolvable, Severity: "error",
			Message: "spi: no chip-select assertion found", Channel: csList[0]}
	}

	sclkT := p.Times[sclk]
	sclkL := p.Levels[sclk]
	mosiT := p.Times[mosi]

	var cands []*model.Candidate
	for _, mode := range modes {
		cpol := mode >> 1
		cpha := mode & 1
		sampleRising := (cpol ^ cpha) == 0
		cand := &model.Candidate{Protocol: "spi", Reasons: []string{}}
		alt := &model.Candidate{Protocol: "spi", Reasons: []string{model.CodeBoundarySample}}
		altUsed := false
		boundarySeen := false
		for _, fr := range frames {
			tx := model.Transaction{Device: fr.dev, StartNs: fr.start, EndNs: fr.end}
			txAlt := tx
			var bits, bitsAlt []int
			var bitT []int64
			var bitRef []int
			edgeNo := -1
			for i, t := range sclkT {
				if t < fr.start || t >= fr.end {
					continue
				}
				rising := sclkL[i] == 1
				edgeNo++
				isSample := rising == sampleRising
				if cpha == 1 && edgeNo == 0 {
					isSample = false // first edge is a shift edge in CPHA=1
				}
				if !isSample {
					continue
				}
				bit := p.levelAt(mosi, t, 1)
				altBit := bit
				// boundary sampling: MOSI transition exactly at the clock edge
				if idx := sort.Search(len(mosiT), func(k int) bool { return mosiT[k] >= t }); idx < len(mosiT) && mosiT[idx] == t {
					boundarySeen = true
					altBit = 1 - bit
					diags = append(diags, model.Diagnostic{Code: model.CodeBoundarySample, Severity: "warn",
						Message: "MOSI edge simultaneous with sampling clock edge; bit value ambiguous",
						Channel: mosi, TimeNs: t, EdgeIndex: p.EdgeRefs[mosi][idx]})
				}
				bits = append(bits, bit)
				bitsAlt = append(bitsAlt, altBit)
				bitT = append(bitT, t)
				bitRef = append(bitRef, p.EdgeRefs[sclk][i])
			}
			build := func(dst *model.Transaction, bs []int) {
				for b := 0; b+8 <= len(bs); b += 8 {
					v := 0
					for k := 0; k < 8; k++ {
						v = v<<1 | bs[b+k]
					}
					dst.Bytes = append(dst.Bytes, model.Byte{
						Value: v, StartNs: bitT[b], EndNs: bitT[b+7],
						EdgeIdx0: bitRef[b], EdgeIdx1: bitRef[b+7],
					})
				}
			}
			build(&tx, bits)
			if boundarySeen {
				build(&txAlt, bitsAlt)
			}
			if len(tx.Bytes) > 0 {
				cand.Txs = append(cand.Txs, tx)
				if boundarySeen && len(txAlt.Bytes) > 0 {
					txAlt.Bytes = txAlt.Bytes[:len(tx.Bytes)]
					alt.Txs = append(alt.Txs, txAlt)
					altUsed = true
				}
			}
		}
		if len(cand.Txs) == 0 {
			continue
		}
		integral := true
		for _, tx := range cand.Txs {
			_ = tx
		}
		if len(modes) > 1 {
			cand.Reasons = append(cand.Reasons, model.CodeSPIModeUnknown)
			diags = append(diags, model.Diagnostic{Code: model.CodeSPIModeUnknown, Severity: "info",
				Message: fmt.Sprintf("spi mode %d retained as legal candidate", mode)})
		}
		if len(csList) > 1 {
			cand.Reasons = append(cand.Reasons, model.CodeMultiDeviceCS)
		}
		cand.Confidence = 0.7
		if len(modes) == 1 {
			cand.Confidence = 0.9
		}
		if integral {
			cand.Confidence += 0.05
		}
		cand.Reasons = append(cand.Reasons, fmt.Sprintf("mode_%d", mode))
		cands = append(cands, cand)
		if altUsed {
			alt.Confidence = cand.Confidence - 0.25
			alt.Reasons = append([]string{}, cand.Reasons...)
			alt.Reasons = append(alt.Reasons, model.CodeBoundarySample)
			cand.Reasons = append(cand.Reasons, model.CodeBoundarySample)
			cands = append(cands, alt)
		}
	}
	if len(cands) == 0 {
		return nil, diags, &model.Diagnostic{Code: model.CodeUnsolvable, Severity: "error",
			Message: "spi: no complete byte inside any chip-select frame", Channel: sclk}
	}
	return cands, diags, nil
}

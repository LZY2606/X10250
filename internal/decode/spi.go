package decode

import (
	"fmt"
	"sort"

	"edgebench/internal/model"
)

type window struct {
	start int64
	end   int64
}

// activeWindows returns the intervals where a chip-select is asserted.
func activeWindows(cs []model.LEdge, activeLow bool) []window {
	activeLvl := 1
	if activeLow {
		activeLvl = 0
	}
	init := initLevel(cs, !activeLow)
	var ws []window
	open := false
	var start int64
	cur := init
	for _, e := range cs {
		if !open && e.Lvl == activeLvl && cur != activeLvl {
			open = true
			start = e.T
		} else if open && e.Lvl != activeLvl {
			ws = append(ws, window{start, e.T})
			open = false
		}
		cur = e.Lvl
	}
	if open && len(cs) > 0 {
		ws = append(ws, window{start, cs[len(cs)-1].T})
	}
	return ws
}

func spiSampleEdge(mode int) (rising bool) {
	// mode 0/3 sample on the leading edge when clock idles low/high.
	return mode == 0 || mode == 3
}

func decodeSPILane(sclk, data []model.LEdge, w window, mode int, p *model.Params,
	cid, laneCh, dev string, txnIdx int) (model.Transaction, int, int) {
	rising := spiSampleEdge(mode)
	initData := initLevel(data, false)
	tx := model.Transaction{
		ID:      model.HashID("txn", cid, dev, laneCh, txnIdx),
		Kind:    "spi",
		Lane:    laneCh,
		Device:  dev,
		Status:  "complete",
		StartNS: w.start,
		EndNS:   w.end,
	}
	var bits []bitRec
	total, boundary := 0, 0
	emit := func() {
		grp := make([]bitRec, 8)
		copy(grp, bits[:8])
		bits = bits[8:]
		val := 0
		refs := make([]int, 0, 8)
		bnd := false
		for j := 0; j < 8; j++ {
			val = val<<1 | grp[j].val
		}
		for _, b := range grp {
			refs = append(refs, b.sclIdx)
			if b.boundary {
				bnd = true
			}
		}
		tx.Bytes = append(tx.Bytes, model.Byte{
			ID:           model.HashID("byt", tx.ID, len(tx.Bytes)),
			Index:        len(tx.Bytes),
			Value:        val,
			Role:         "word",
			BoundaryRisk: bnd,
			EdgeRefs:     refs,
			StartNS:      grp[0].t,
			EndNS:        grp[7].t,
		})
	}
	for _, e := range sclk {
		if e.T < w.start || e.T > w.end {
			continue
		}
		if (e.Lvl == 1) != rising {
			continue
		}
		v := levelAt(data, e.T, initData)
		b := nearestDist(data, e.T) <= p.SampleOffsetNS
		total++
		if b {
			boundary++
		}
		bits = append(bits, bitRec{val: v, t: e.T, sclIdx: e.Idx, boundary: b})
		if len(bits) == 8 {
			emit()
		}
	}
	if len(bits) > 0 {
		tx.Status = "truncated"
		tx.Annotations = append(tx.Annotations, model.Annotation{
			Type: "partial_byte", AtNS: bits[0].t,
			Note: fmt.Sprintf("%d dangling bit(s) at CS deassert", len(bits))})
	}
	return tx, total, boundary
}

func decodeSPI(sclk, mosi, miso []model.LEdge, mosiCh, misoCh string,
	css map[string][]model.LEdge, p *model.Params, decID string) ([]model.Candidate, []model.Judgment) {

	modes := []int{0, 1, 2, 3}
	if p.SPIMode != "unknown" {
		m := 0
		if _, err := fmt.Sscanf(p.SPIMode, "%d", &m); err == nil && m >= 0 && m <= 3 {
			modes = []int{m}
		}
	}

	devs := make([]string, 0, len(css))
	for ch := range css {
		devs = append(devs, ch)
	}
	sort.Strings(devs)

	var cands []model.Candidate
	for _, mode := range modes {
		cid := model.HashID("cnd", decID, "spi", fmt.Sprintf("mode%d", mode))
		cand := model.Candidate{
			ID:         cid,
			Protocol:   "spi",
			Hypothesis: fmt.Sprintf("spi/mode%d", mode),
		}
		total, boundary := 0, 0
		txnIdx := 0
		for _, dev := range devs {
			for _, w := range activeWindows(css[dev], p.CSActiveLow) {
				if mosiCh != "" {
					tx, tt, bb := decodeSPILane(sclk, mosi, w, mode, p, cid, mosiCh, dev, txnIdx)
					txnIdx++
					total += tt
					boundary += bb
					if len(tx.Bytes) > 0 {
						cand.Transactions = append(cand.Transactions, tx)
					}
				}
				if misoCh != "" {
					tx, tt, bb := decodeSPILane(sclk, miso, w, mode, p, cid, misoCh, dev, txnIdx)
					txnIdx++
					total += tt
					boundary += bb
					if len(tx.Bytes) > 0 {
						cand.Transactions = append(cand.Transactions, tx)
					}
				}
			}
		}
		if len(cand.Transactions) == 0 {
			continue
		}
		conf := 0.0
		if total > 0 {
			conf = round2(1.0 - float64(boundary)/float64(total))
		}
		cand.Confidence = conf
		cands = append(cands, cand)
	}

	sort.SliceStable(cands, func(a, b int) bool {
		if cands[a].Confidence != cands[b].Confidence {
			return cands[a].Confidence > cands[b].Confidence
		}
		return cands[a].Hypothesis < cands[b].Hypothesis
	})

	var judgments []model.Judgment
	if p.SPIMode == "unknown" && len(cands) > 1 {
		ids := make([]string, 0, len(cands))
		for _, c := range cands {
			ids = append(ids, c.ID)
		}
		judgments = append(judgments, model.Judgment{
			Rule:    model.RuleUnknownSPIMode,
			Subject: ids[0],
			Basis:   fmt.Sprintf("spi_mode=unknown; %d mode hypotheses retained with confidence from sample-point stability", len(cands)),
			Adopted: true,
		})
	}
	return cands, judgments
}

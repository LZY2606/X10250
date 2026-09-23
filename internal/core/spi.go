package core

import (
	"fmt"
	"sort"
	"strings"
)

// levelAt 返回通道在时刻 t 的电平（最后一个不晚于 t 的边沿），无则返回 def。
func levelAt(edges []Edge, t int64, def int) int {
	lv := def
	for _, e := range edges {
		if e.TPS > t {
			break
		}
		lv = e.Level
	}
	return lv
}

func nearBoundary(times []int64, t, window int64) bool {
	for _, x := range times {
		d := x - t
		if d < 0 {
			d = -d
		}
		if d <= window {
			return true
		}
	}
	return false
}

// spiSampleRising 各 mode 的采样沿方向（CPOL/CPHA 标准定义）。
func spiSampleRising(mode int) bool {
	return mode == 0 || mode == 3
}

// DecodeSPI 译码 SPI。多设备片选各自成帧；mode 不明时保留全部合法候选；
// 边界采样产生二义位候选；资源预算受 Budget 约束。
func DecodeSPI(sck, mosi, miso []Edge, csByDevice map[string][]Edge, p SPIParams, budget *Budget) ([]Transaction, []Diagnostic) {
	sort.SliceStable(sck, func(i, j int) bool { return sck[i].TPS < sck[j].TPS })
	var mosiTimes, misoTimes []int64
	for _, e := range mosi {
		mosiTimes = append(mosiTimes, e.TPS)
	}
	for _, e := range miso {
		misoTimes = append(misoTimes, e.TPS)
	}
	modes := []int{p.Mode}
	priors := map[int]float64{p.Mode: 1}
	if p.Mode < 0 {
		modes = []int{0, 1, 2, 3}
		priors = map[int]float64{0: 0.35, 1: 0.15, 2: 0.15, 3: 0.35}
	}
	devices := make([]string, 0, len(csByDevice))
	for dev := range csByDevice {
		devices = append(devices, dev)
	}
	sort.Strings(devices)

	var txs []Transaction
	var diags []Diagnostic
	for _, dev := range devices {
		csEdges := append([]Edge(nil), csByDevice[dev]...)
		sort.SliceStable(csEdges, func(i, j int) bool { return csEdges[i].TPS < csEdges[j].TPS })
		inactive := 1 - p.CSActiveLevel
		var openAt int64
		open := false
		type frame struct{ start, end int64 }
		var frames []frame
		for _, e := range csEdges {
			if e.Level == p.CSActiveLevel && !open {
				open = true
				openAt = e.TPS
			} else if e.Level == inactive && open {
				open = false
				frames = append(frames, frame{openAt, e.TPS})
			}
		}
		if open {
			frames = append(frames, frame{openAt, sck[len(sck)-1].TPS + 1})
		}
		for _, fr := range frames {
			tx, td := decodeSPIFrame(dev, fr.start, fr.end, sck, mosi, miso, mosiTimes, misoTimes, modes, priors, p, budget)
			if tx != nil {
				txs = append(txs, *tx)
			}
			diags = append(diags, td...)
		}
	}
	return txs, diags
}

func decodeSPIFrame(dev string, start, end int64, sck, mosi, miso []Edge, mosiTimes, misoTimes []int64, modes []int, priors map[int]float64, p SPIParams, budget *Budget) (*Transaction, []Diagnostic) {
	var in []Edge
	var edgeIDs []string
	for _, e := range sck {
		if e.TPS >= start && e.TPS <= end {
			in = append(in, e)
			edgeIDs = append(edgeIDs, e.ID)
		}
	}
	id := ShortID("TX-", "spi", dev, start, end, strings.Join(edgeIDs, ","), p.Mode, p.BitsPerWord)
	if len(in) == 0 {
		dg := Diagnostic{Layer: "decode", Pos: id, Severity: "error", Code: "no_clock",
			Message: fmt.Sprintf("设备 %s 的片选帧 [%s, %s] 内没有 SCK 边沿，语义无解", dev, FormatPS(start), FormatPS(end))}
		return &Transaction{
			ID: id, Protocol: "spi", Device: dev, StartPS: start, EndPS: end,
			Status: StatusUnsolvable, Diagnostics: []Diagnostic{dg},
			Variants: []Variant{{ID: id + ":V:none", Label: "none", Confidence: 0}},
		}, nil
	}
	var diags []Diagnostic
	var all []Variant
	blownAll := false
	for _, mode := range modes {
		rising := spiSampleRising(mode)
		var mosiBits, misoBits []Bit
		for _, e := range in {
			isRising := e.Level == 1
			if isRising != rising {
				continue
			}
			mb := Bit{Value: levelAt(mosi, e.TPS, 0), ClockEdgeID: e.ID,
				Ambiguous: nearBoundary(mosiTimes, e.TPS, p.BoundaryWindowPS)}
			sb := Bit{Value: levelAt(miso, e.TPS, 0), ClockEdgeID: e.ID,
				Ambiguous: nearBoundary(misoTimes, e.TPS, p.BoundaryWindowPS)}
			mosiBits = append(mosiBits, mb)
			misoBits = append(misoBits, sb)
		}
		opts, d := spiWordOpts(id, "mosi", mosiBits, p.BitsPerWord)
		diags = append(diags, d...)
		optsMiso, d2 := spiWordOpts(id, "miso", misoBits, p.BitsPerWord)
		diags = append(diags, d2...)
		opts = append(opts, optsMiso...)
		vs, blown := buildVariants(id, opts, budget)
		if blown {
			blownAll = true
		}
		for i := range vs {
			lbl := fmt.Sprintf("mode%d", mode)
			if vs[i].Label != "base" {
				lbl += "+" + vs[i].Label
			}
			vs[i].Label = lbl
			vs[i].ID = id + ":V:" + lbl
			for j := range vs[i].Bytes {
				vs[i].Bytes[j].ID = fmt.Sprintf("%s:B%d", vs[i].ID, vs[i].Bytes[j].Index)
				if vs[i].Bytes[j].Lane != "" {
					vs[i].Bytes[j].ID = fmt.Sprintf("%s:%s%d", vs[i].ID, vs[i].Bytes[j].Lane, vs[i].Bytes[j].Index)
				}
			}
			vs[i].Confidence *= priors[mode]
		}
		all = append(all, vs...)
	}
	// 跨 mode 归一置信，稳定排序。
	var sum float64
	for _, v := range all {
		sum += v.Confidence
	}
	if sum == 0 {
		sum = 1
	}
	for i := range all {
		all[i].Confidence /= sum
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Confidence != all[j].Confidence {
			return all[i].Confidence > all[j].Confidence
		}
		return all[i].Label < all[j].Label
	})
	if blownAll {
		diags = append(diags, Diagnostic{Layer: "decode", Pos: id, Severity: "error", Code: "budget_exhausted",
			Message: "候选数量超过资源预算：保留已生成候选并标记预算耗尽"})
	}
	status := StatusOK
	switch {
	case blownAll:
		status = StatusBudgetExhausted
	case len(all) > 1:
		status = StatusAmbiguous
	}
	if len(all) == 0 {
		all = []Variant{{ID: id + ":V:base", Label: "base", Confidence: 1}}
	}
	return &Transaction{
		ID: id, Protocol: "spi", Device: dev, StartPS: start, EndPS: end,
		Status: status, Variants: all, Diagnostics: diags, EdgeIDs: edgeIDs,
	}, diags
}

// spiWordOpts 把一条通道的采样位打包成字，并处理部分字与二义位。
func spiWordOpts(txID, lane string, bits []Bit, word int) ([][]byteOpt, []Diagnostic) {
	var opts [][]byteOpt
	var diags []Diagnostic
	for start := 0; start < len(bits); start += word {
		end := start + word
		if end > len(bits) {
			end = len(bits)
		}
		chunk := bits[start:end]
		idx := start / word
		mkByte := func(value int) ByteCand {
			bc := ByteCand{Index: idx, Lane: lane, Value: value, Confidence: 1,
				Bits: append([]Bit(nil), chunk...)}
			for _, b := range chunk {
				if b.ClockEdgeID != "" {
					bc.EdgeIDs = append(bc.EdgeIDs, b.ClockEdgeID)
				}
			}
			return bc
		}
		if len(chunk) < word {
			diags = append(diags, Diagnostic{Layer: "decode", Pos: fmt.Sprintf("%s:%s%d", txID, lane, idx),
				Severity: "warning", Code: "partial_word",
				Message: fmt.Sprintf("%s 第 %d 字仅 %d/%d 位（多设备片选交叠截断），保留残缺字不补全", lane, idx+1, len(chunk), word)})
			val := 0
			for _, b := range chunk {
				val = (val << 1) | b.Value
			}
			bc := mkByte(val)
			bc.Notes = append(bc.Notes, "partial_word")
			opts = append(opts, []byteOpt{{cand: bc, weight: 1}})
			continue
		}
		ambIdx := -1
		for i, b := range chunk {
			if b.Ambiguous {
				ambIdx = i
			}
		}
		base := 0
		for _, b := range chunk {
			base = (base << 1) | b.Value
		}
		if ambIdx < 0 {
			opts = append(opts, []byteOpt{{cand: mkByte(base), weight: 1}})
			continue
		}
		v0 := base &^ (1 << (word - 1 - ambIdx))
		v1 := v0 | 1<<(word-1-ambIdx)
		diags = append(diags, Diagnostic{Layer: "decode", Pos: fmt.Sprintf("%s:%s%d", txID, lane, idx),
			Severity: "warning", Code: "boundary_sample",
			Message: fmt.Sprintf("%s 第 %d 字第 %d 位位于采样边界，保留两个候选", lane, idx+1, ambIdx+1)})
		b0 := mkByte(v0)
		b0.Notes = append(b0.Notes, "boundary_sample")
		b1 := mkByte(v1)
		b1.Notes = append(b1.Notes, "boundary_sample")
		opts = append(opts, []byteOpt{
			{cand: b0, tag: fmt.Sprintf("%sw%db%d=0", lane, idx, ambIdx), weight: 0.5},
			{cand: b1, tag: fmt.Sprintf("%sw%db%d=1", lane, idx, ambIdx), weight: 0.5},
		})
	}
	return opts, diags
}

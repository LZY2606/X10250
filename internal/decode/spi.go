package decode

import (
	"fmt"

	"edgebench/internal/model"
)

var spiModeSampleRises = map[int]bool{0: true, 1: false, 2: false, 3: true}

type modeRun struct {
	mode int
	out  coreOut
}

func decodeSPI(m *model.Material, p model.DecodeParams, prep *prepared) decoderOutput {
	gapFactor, stretchFactor := p.GapFactor, p.StretchFactor
	if gapFactor <= 0 {
		gapFactor = 8
	}
	if stretchFactor <= 0 {
		stretchFactor = 3
	}
	diags := missingRoleDiag(map[string]bool{"clk": true, "data": true}, m, prep)
	if len(diags) > 0 {
		return decoderOutput{diags: diags, unsolvable: true}
	}
	clk, data := prep.byRole["clk"], prep.byRole["data"]
	if len(clk) == 0 {
		return decoderOutput{unsolvable: true, diags: []model.Diagnostic{{
			Code: "no_clock_edges", Message: "SPI 时钟通道无边沿，无法采样任何数据位", EdgeIndex: -1, Channel: -1,
		}}}
	}

	spanStart, spanEnd := clk[0].t, clk[len(clk)-1].t
	ws := buildWindows(prep.csEdges, spanStart, spanEnd)
	lsb := p.BitOrder == "lsb"

	csList := make([]int, 0, len(prep.csEdges))
	for ch := range prep.csEdges {
		csList = append(csList, ch)
	}
	sortInts(csList)
	multiEvs := []model.DecodeEvent{}
	if len(csList) > 1 {
		multiEvs = append(multiEvs, model.DecodeEvent{
			Type:      "multi_cs_select",
			TimeNs:    spanStart,
			EdgeIndex: -1,
			Detail:    mustJSON(map[string]any{"cs_channels": csList, "devices": len(csList)}),
		})
	}

	var modes []int
	switch p.SPIMode {
	case "", "unknown":
		modes = []int{0, 1, 2, 3}
		multiEvs = append(multiEvs, model.DecodeEvent{
			Type: "spi_mode_ambiguous", TimeNs: spanStart, EdgeIndex: -1,
			Detail: "CPOL/CPHA 未指定，保留 4 个 mode 候选，不假定默认值",
		})
	default:
		mode := int(p.SPIMode[0] - '0')
		if mode < 0 || mode > 3 {
			return decoderOutput{unsolvable: false, diags: []model.Diagnostic{{
				Code: "bad_spi_mode", Message: "SPI mode 必须为 unknown 或 0..3，收到: " + p.SPIMode, EdgeIndex: -1, Channel: -1,
			}}}
		}
		modes = []int{mode}
	}

	runs := make([]modeRun, 0, 4)
	for _, mode := range modes {
		o := clockBusCore(clk, data, ws, clockBusOpts{
			sampleRises:   spiModeSampleRises[mode],
			gapFactor:     gapFactor,
			stretchFactor: stretchFactor,
			lsbFirst:      lsb,
			res:           resolver{},
			busKind:       "spi_transfer",
		})
		runs = append(runs, modeRun{mode: mode, out: o})
	}
	if diag := firstUnsolvable(runs, modes); diag != nil {
		return decoderOutput{diags: []model.Diagnostic{*diag}, unsolvable: true}
	}

	var out decoderOutput
	emit := func(mode int, co coreOut, key, label string, penalty float64) {
		evs := append(append([]model.DecodeEvent{}, multiEvs...), co.evs...)
		evs = append(evs, model.DecodeEvent{
			Type: "spi_mode_used", EdgeIndex: -1, TimeNs: spanStart,
			Detail: mustJSON(map[string]any{"mode": mode, "key": key}),
		})
		out.specs = append(out.specs, candidateSpec{
			key: key, label: label, penalty: penalty, txns: co.txns,
			events: withKept(evs, len(co.ambs)),
		})
	}

	base := runs[0]
	emit(base.mode, base.out, "mode=0;base", fmt.Sprintf("SPI mode 0（CPOL=0/CPHA=0）：%d 条传输", len(base.out.txns)), 0)
	// 基线歧义点逐个单翻转（边界采样 / 缺边），最多补到 8 候选。
	for i, a := range base.out.ambs {
		if len(out.specs) >= 8 {
			break
		}
		alt := clockBusCore(clk, data, ws, clockBusOpts{
			sampleRises: true, gapFactor: gapFactor, stretchFactor: stretchFactor,
			lsbFirst: lsb, res: resolver{i: 1}, busKind: "spi_transfer",
		})
		key := fmt.Sprintf("mode=0;%s@%s=%s", a.kind, model.Digest("amb", i, a.t, a.edge)[:8], a.opts[1])
		label := fmt.Sprintf("mode 0 候选：%s 取 %s @ %s", a.kind, a.opts[1], formatNs(a.t))
		emit(0, alt, key, label, 0.2)
	}
	for _, r := range runs[1:] {
		if len(out.specs) >= 8 {
			break
		}
		key := fmt.Sprintf("mode=%d;base", r.mode)
		label := fmt.Sprintf("SPI mode %d：%d 条传输（mode 不明的并列候选）", r.mode, len(r.out.txns))
		emit(r.mode, r.out, key, label, 0.1+0.05*float64(r.mode))
	}
	return out
}

func firstUnsolvable(runs []modeRun, modes []int) *model.Diagnostic {
	_ = modes
	for _, r := range runs {
		if len(r.out.txns) == 0 {
			return &model.Diagnostic{
				Code: "no_sample_edges",
				Message: fmt.Sprintf("mode %d 在片选窗口内未采到任何完整字节", r.mode),
				EdgeIndex: -1, Channel: -1,
			}
		}
	}
	return nil
}

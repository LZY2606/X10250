package decode

import (
	"edgebench/internal/model"
)

// decodeSync 处理自定义同步总线：时钟 + 数据，极性来自探头配置或参数修订（Polarity 覆盖）。
func decodeSync(m *model.Material, p model.DecodeParams, prep *prepared) decoderOutput {
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
	polarity := p.Polarity
	if polarity == "" {
		polarity = m.Probe.ClockPolarity
	}
	if polarity != "rising" && polarity != "falling" {
		return decoderOutput{unsolvable: true, diags: []model.Diagnostic{{
			Code: "missing_polarity",
			Message: "自定义同步总线缺少时钟极性（探头配置与参数修订均未提供），拒绝假定默认值",
			EdgeIndex: -1, Channel: -1,
		}}}
	}
	clk, data := prep.byRole["clk"], prep.byRole["data"]
	if len(clk) == 0 {
		return decoderOutput{unsolvable: true, diags: []model.Diagnostic{{
			Code: "no_clock_edges", Message: "同步时钟通道无边沿", EdgeIndex: -1, Channel: -1,
		}}}
	}
	spanStart, spanEnd := clk[0].t, clk[len(clk)-1].t
	ws := []window{{cs: -1, start: spanStart, end: spanEnd}}
	lsb := p.BitOrder == "lsb"
	base := clockBusCore(clk, data, ws, clockBusOpts{
		sampleRises: polarity == "rising", gapFactor: gapFactor, stretchFactor: stretchFactor,
		lsbFirst: lsb, res: resolver{}, busKind: "sync_word",
	})
	if len(base.txns) == 0 {
		return decoderOutput{unsolvable: true, diags: []model.Diagnostic{{
			Code: "no_sync_words", Message: "在给定极性下未重组出任何完整字", EdgeIndex: -1, Channel: -1,
		}}}
	}
	out := decoderOutput{}
	key := "polarity=" + polarity + ";base"
	label := "同步基线：" + polarity + " 沿采样，" + itoa(len(base.txns)) + " 组字"
	out.specs = append(out.specs, candidateSpec{
		key: key, label: label, penalty: 0, txns: base.txns,
		events: withKept(base.evs, len(base.ambs)),
	})
	for i, a := range base.ambs {
		if len(out.specs) >= 8 {
			break
		}
		alt := clockBusCore(clk, data, ws, clockBusOpts{
			sampleRises: polarity == "rising", gapFactor: gapFactor, stretchFactor: stretchFactor,
			lsbFirst: lsb, res: resolver{i: 1}, busKind: "sync_word",
		})
		vk := key + ";" + a.kind + "@" + model.Digest("amb", i, a.t, a.edge)[:8] + "=" + a.opts[1]
		out.specs = append(out.specs, candidateSpec{
			key: vk,
			label: "同步候选：" + a.kind + " 取 " + a.opts[1] + " @ " + formatNs(a.t),
			penalty: 0.2, txns: alt.txns, events: withKept(alt.evs, 1),
		})
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// Package decode 实现 I2C / SPI / 自定义同步边沿解码。
// 输入为不可变 Capture + 参数版本；输出为内容寻址 Result。
// 关键规则：时钟缺边、重复 START、clock stretching、SPI mode 不明时保留多个解码候选；
// 数据缺口不拼接成交易。
package decode

import (
	"edgebench/internal/domain"
)

// levelAt 返回通道在时刻 t 的有效电平（t 之前最后一个边沿的电平；无边沿返回 def）。
func levelAt(edges []domain.Edge, t int64, def int) int {
	lvl := def
	for _, e := range edges {
		if e.T > t {
			break
		}
		if e.EffectiveLevel >= 0 {
			lvl = e.EffectiveLevel
		}
	}
	return lvl
}

// lastEdgeAtOrBefore 返回 t 及之前最后一个边沿。
func lastEdgeAtOrBefore(edges []domain.Edge, t int64) *domain.Edge {
	var out *domain.Edge
	for i := range edges {
		if edges[i].T > t {
			break
		}
		out = &edges[i]
	}
	return out
}

// transition 边沿方向。
type transition struct {
	Edge  domain.Edge
	Rise  bool // 0->1
	Fall  bool // 1->0
	Index int
}

// transitions 由边沿序列推导方向（首边沿以 prevLevel 为前级）。
func transitions(edges []domain.Edge, prevLevel int) []transition {
	out := make([]transition, 0, len(edges))
	prev := prevLevel
	for i, e := range edges {
		if e.EffectiveLevel < 0 {
			continue // 无法转换的电平留在原位，不参与方向推导
		}
		tr := transition{Edge: e, Index: i}
		if prev == 0 && e.EffectiveLevel == 1 {
			tr.Rise = true
		} else if prev == 1 && e.EffectiveLevel == 0 {
			tr.Fall = true
		} else {
			// 与推导前级相同：按边沿即翻转的惯例处理
			if e.EffectiveLevel == 1 {
				tr.Rise = true
			} else {
				tr.Fall = true
			}
		}
		out = append(out, tr)
		prev = e.EffectiveLevel
	}
	return out
}

// assembleByte 按位序组装字节。
func assembleByte(bits []domain.Bit, order string, width int) int {
	v := 0
	if order == "lsb" {
		for i, b := range bits {
			if i >= width {
				break
			}
			v |= (b.Value & 1) << i
		}
		return v
	}
	n := len(bits)
	if n > width {
		n = width
	}
	for i := 0; i < n; i++ {
		v = (v << 1) | (bits[i].Value & 1)
	}
	return v
}

// inputHash 结果输入摘要：capture + 参数 + 解码器版本。
func inputHash(c *domain.Capture, paramID, decVer, proto string) string {
	return domain.HashStrings(c.ID, paramID, decVer, proto)
}

// candidateKey 候选内容键，用于结果内容寻址。
func candidateKey(cands []domain.Candidate) string {
	h := ""
	for _, c := range cands {
		h += c.Label + "|" + c.Assumption + "|"
		for _, tx := range c.Transactions {
			for _, b := range tx.Bytes {
				h += b.Hex + ","
			}
			h += ";"
		}
		h += "#"
	}
	return domain.HashStrings(h)
}

// splitByGap 判断字节区间是否跨越缺口。
func gapBetween(c *domain.Capture, a, b int64) (domain.Gap, bool) {
	return c.GapCrossed(a, b)
}

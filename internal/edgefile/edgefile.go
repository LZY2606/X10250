// Package edgefile 解析 .edge 文本采集格式。
// 解析失败分三类：format-corrupt（格式损坏）、semantic-unsolvable（语义无解）、
// budget-exhausted（资源预算耗尽），全部带行列位置诊断。
package edgefile

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"edgebench/internal/domain"
)

// Budget 资源预算：超限返回 budget-exhausted，而不是静默截断。
type Budget struct {
	MaxEdges    int
	MaxChannels int
	MaxLineLen  int
}

func DefaultBudget() Budget {
	return Budget{MaxEdges: 200000, MaxChannels: 64, MaxLineLen: 4096}
}

var rawLevelNorm = map[string]int{
	"0": 0, "1": 1,
	"l": 0, "h": 1,
	"low": 0, "high": 1,
	"lo": 0, "hi": 1,
}

// ParseWithDiags 解析 .edge 文本为不可变 Capture，并返回非致命诊断。
// 原始电平表示保留在 Edge.RawLevel；EffectiveLevel 应用通道反相后固定。
func ParseWithDiags(name, src string, r io.Reader, budget Budget) (*domain.Capture, []domain.Diag, *domain.DiagError) {
	if budget.MaxEdges == 0 {
		budget = DefaultBudget()
	}
	c := &domain.Capture{Name: name, Source: src, TimeUnit: "ns", RateUnit: "Hz"}
	chByAlias := map[string]*domain.Channel{}
	var diags []domain.Diag

	fail := func(kind string, line, col int, ctx, msg string) *domain.DiagError {
		return &domain.DiagError{Kind: kind, Diags: []domain.Diag{{Kind: kind, File: src, Line: line, Col: col, Context: ctx, Message: msg}}}
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), budget.MaxLineLen+1)
	lineNo := 0
	edgeCount := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if len(line) > budget.MaxLineLen {
			return nil, nil, fail("budget-exhausted", lineNo, budget.MaxLineLen, "line", fmt.Sprintf("行长 %d 超过预算 %d", len(line), budget.MaxLineLen))
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		kw := strings.ToLower(fields[0])
		switch kw {
		case "capture":
			if len(fields) >= 2 {
				c.Name = fields[1]
			}
		case "rate":
			if len(fields) < 3 {
				return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "rate 需要 <数值> <单位>")
			}
			v, err := strconv.ParseFloat(fields[1], 64)
			if err != nil || v <= 0 {
				return nil, nil, fail("format-corrupt", lineNo, 6, trimmed, "采样率数值无效: "+fields[1])
			}
			c.SampleRate, c.RateUnit = v, fields[2]
		case "timeunit":
			if len(fields) < 2 {
				return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "timeunit 需要单位")
			}
			c.TimeUnit = fields[1]
		case "channel":
			// channel <alias> <role> [invert] [cs-active-low|cs-active-high]
			if len(fields) < 3 {
				return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "channel 需要 <别名> <角色>")
			}
			if len(c.Channels) >= budget.MaxChannels {
				return nil, nil, fail("budget-exhausted", lineNo, 1, trimmed, fmt.Sprintf("通道数超过预算 %d", budget.MaxChannels))
			}
			alias, role := fields[1], strings.ToLower(fields[2])
			if _, dup := chByAlias[alias]; dup {
				return nil, nil, fail("semantic-unsolvable", lineNo, 9, trimmed, "通道别名重复: "+alias)
			}
			switch role {
			case "sda", "scl", "mosi", "miso", "sck", "cs", "data", "clk":
			default:
				return nil, nil, fail("semantic-unsolvable", lineNo, 9+len(alias)+1, trimmed, "未知通道角色: "+role)
			}
			ch := domain.Channel{Alias: alias, Role: role, CSActive: 0}
			for _, opt := range fields[3:] {
				switch strings.ToLower(opt) {
				case "invert":
					ch.Inverted = true
				case "cs-active-low":
					ch.CSActive = 0
				case "cs-active-high":
					ch.CSActive = 1
				default:
					return nil, nil, fail("format-corrupt", lineNo, strings.Index(line, opt)+1, trimmed, "未知通道选项: "+opt)
				}
			}
			ch.ID = domain.StableID("ch", c.Name, alias, role)
			c.Channels = append(c.Channels, ch)
			chByAlias[alias] = &c.Channels[len(c.Channels)-1]
		case "gap":
			// gap <start> <end> [note...]
			if len(fields) < 3 {
				return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "gap 需要 <start> <end>")
			}
			a, err1 := strconv.ParseInt(fields[1], 10, 64)
			b, err2 := strconv.ParseInt(fields[2], 10, 64)
			if err1 != nil || err2 != nil {
				return nil, nil, fail("format-corrupt", lineNo, 5, trimmed, "gap 时间无效")
			}
			if b <= a {
				return nil, nil, fail("semantic-unsolvable", lineNo, 5, trimmed, "gap 结束必须大于起始")
			}
			note := ""
			if len(fields) > 3 {
				note = strings.Join(fields[3:], " ")
			}
			c.Gaps = append(c.Gaps, domain.Gap{Start: a, End: b, Unit: c.TimeUnit, Note: note})
		case "edge":
			// edge <alias> <t> <level>
			if len(fields) < 4 {
				return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "edge 需要 <通道> <时间> <电平>")
			}
			if edgeCount >= budget.MaxEdges {
				return nil, nil, fail("budget-exhausted", lineNo, 1, trimmed, fmt.Sprintf("边沿数超过预算 %d", budget.MaxEdges))
			}
			ch, ok := chByAlias[fields[1]]
			if !ok {
				return nil, nil, fail("semantic-unsolvable", lineNo, 6, trimmed, "边沿引用了未声明的通道: "+fields[1])
			}
			t, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				return nil, nil, fail("format-corrupt", lineNo, strings.Index(line, fields[2])+1, trimmed, "边沿时间无效: "+fields[2])
			}
			raw := fields[3]
			lvl, ok := rawLevelNorm[strings.ToLower(raw)]
			if !ok {
				// 无法转换的值留在原位置并附诊断，不丢弃、不伪装
				diags = append(diags, domain.Diag{Kind: "semantic-unsolvable", File: src, Line: lineNo, Col: strings.Index(line, raw) + 1, Context: trimmed, Message: "无法识别的电平表示，已保留原始值: " + raw})
				lvl = -1
			}
			eff := lvl
			if lvl >= 0 && ch.Inverted {
				eff = 1 - lvl
			}
			e := domain.Edge{
				ID:             domain.StableID("edg", ch.ID, strconv.FormatInt(t, 10), raw, strconv.Itoa(edgeCount)),
				ChannelID:      ch.ID,
				T:              t,
				Unit:           c.TimeUnit,
				RawLevel:       raw,
				EffectiveLevel: eff,
			}
			c.Edges = append(c.Edges, e)
			edgeCount++
		default:
			return nil, nil, fail("format-corrupt", lineNo, 1, trimmed, "未知指令: "+fields[0])
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, fail("format-corrupt", lineNo, 1, "", "读取失败: "+err.Error())
	}
	if c.SampleRate <= 0 {
		return nil, nil, fail("semantic-unsolvable", 0, 0, src, "缺少有效的 rate 采样率声明")
	}
	if len(c.Channels) == 0 {
		return nil, nil, fail("semantic-unsolvable", 0, 0, src, "缺少 channel 声明")
	}
	sortEdges(c)
	if bad := checkMonotonic(c); bad != nil {
		return nil, nil, &domain.DiagError{Kind: "semantic-unsolvable", Diags: []domain.Diag{*bad}}
	}
	c.ID = domain.StableID("cap", c.Name, fmt.Sprint(c.SampleRate), c.RateUnit, c.TimeUnit, edgeDigest(c))
	return c, diags, nil
}

func sortEdges(c *domain.Capture) {
	idx := map[string]int{}
	for i := range c.Edges {
		idx[c.Edges[i].ID] = i
	}
	less := func(a, b domain.Edge) bool {
		if a.ChannelID != b.ChannelID {
			return a.ChannelID < b.ChannelID
		}
		if a.T != b.T {
			return a.T < b.T
		}
		return idx[a.ID] < idx[b.ID]
	}
	for i := 1; i < len(c.Edges); i++ {
		for j := i; j > 0 && less(c.Edges[j], c.Edges[j-1]); j-- {
			c.Edges[j], c.Edges[j-1] = c.Edges[j-1], c.Edges[j]
		}
	}
}

func checkMonotonic(c *domain.Capture) *domain.Diag {
	prev := map[string]int64{}
	for _, e := range c.Edges {
		if p, ok := prev[e.ChannelID]; ok && e.T < p {
			return &domain.Diag{Kind: "semantic-unsolvable", Context: e.ID, Message: fmt.Sprintf("通道边沿时间倒退: %d < %d", e.T, p)}
		}
		prev[e.ChannelID] = e.T
	}
	return nil
}

func edgeDigest(c *domain.Capture) string {
	var b strings.Builder
	for _, ch := range c.Channels {
		fmt.Fprintf(&b, "ch|%s|%s|%v|%d;", ch.Alias, ch.Role, ch.Inverted, ch.CSActive)
	}
	for _, e := range c.Edges {
		fmt.Fprintf(&b, "e|%s|%d|%s|%d;", e.ChannelID, e.T, e.RawLevel, e.EffectiveLevel)
	}
	for _, g := range c.Gaps {
		fmt.Fprintf(&b, "g|%d|%d;", g.Start, g.End)
	}
	return b.String()
}

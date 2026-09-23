// Package decode 是业务规则的服务端实现：I2C、SPI 与自定义同步总线的边沿重组。
// 浏览器不持有任何判定逻辑，本包可被测试直接调用。
package decode

import (
	"encoding/json"
	"fmt"
	"sort"

	"edgebench/internal/model"
)

// cedge 是派生层边沿（已应用电平反相）。idx 指向 Material.Edges 中的原始序号。
type cedge struct {
	idx   int
	t     int64
	level int
}

type prepared struct {
	byRole  map[string][]cedge
	csEdges map[int][]cedge
	csRoles map[int]string
	diags   []model.Diagnostic
	corrupt bool
}

// prepare 在派生层应用电平反相并按 (时间, 通道) 规范化排序；原始材料不被修改。
// 等价输入（边沿切片顺序不同但内容相同）在此收敛为同一派生序列。
func prepare(m *model.Material) *prepared {
	p := &prepared{
		byRole:  map[string][]cedge{},
		csEdges: map[int][]cedge{},
		csRoles: map[int]string{},
	}
	cfg := map[int]model.ChannelConfig{}
	for _, c := range m.Probe.Channels {
		cfg[c.Index] = c
	}
	grouped := map[int][]cedge{}
	for i, e := range m.Edges {
		switch {
		case e.Level != 0 && e.Level != 1:
			p.diags = append(p.diags, model.Diagnostic{
				Code: "bad_level", Message: "电平必须为 0 或 1，无法进入派生层",
				Channel: e.Channel, EdgeIndex: i, TimeNs: e.TimeNs,
			})
			p.corrupt = true
			continue
		case e.TimeNs < 0:
			p.diags = append(p.diags, model.Diagnostic{
				Code: "negative_time", Message: "边沿时间为负，捕获坐标系不允许该值",
				Channel: e.Channel, EdgeIndex: i, TimeNs: e.TimeNs,
			})
			p.corrupt = true
			continue
		}
		c, ok := cfg[e.Channel]
		if !ok {
			p.diags = append(p.diags, model.Diagnostic{
				Code: "unmapped_channel", Message: "边沿引用了探头配置中不存在的通道，已留在原位置不参与解码",
				Channel: e.Channel, EdgeIndex: i, TimeNs: e.TimeNs,
			})
			continue
		}
		lvl := e.Level
		if c.Inverted {
			lvl = 1 - lvl
		}
		grouped[e.Channel] = append(grouped[e.Channel], cedge{idx: i, t: e.TimeNs, level: lvl})
	}
	for ch, es := range grouped {
		sort.SliceStable(es, func(i, j int) bool {
			if es[i].t != es[j].t {
				return es[i].t < es[j].t
			}
			return es[i].idx < es[j].idx
		})
		for i := 1; i < len(es); i++ {
			if es[i].t < es[i-1].t {
				// 排序后仍出现回退意味着源数据结构损坏。
				p.diags = append(p.diags, model.Diagnostic{
					Code: "time_regression", Message: "同通道边沿时间非单调，源数据结构损坏",
					Channel: ch, EdgeIndex: es[i].idx, TimeNs: es[i].t,
				})
				p.corrupt = true
			}
		}
		grouped[ch] = es
	}
	for _, c := range m.Probe.Channels {
		es := grouped[c.Index]
		if c.Role == "cs" {
			p.csEdges[c.Index] = es
			p.csRoles[c.Index] = c.Alias
			continue
		}
		if _, exists := p.byRole[c.Role]; !exists {
			p.byRole[c.Role] = es
		}
	}
	return p
}

// levelAt 返回 t 时刻电平：取时间不晚于 t 的最后一条边沿；t 之前无边沿时假定为首个跳变的反相。
func levelAt(edges []cedge, t int64) int {
	i := sort.Search(len(edges), func(i int) bool { return edges[i].t > t })
	if i == 0 {
		if len(edges) == 0 {
			return 0
		}
		return 1 - edges[0].level
	}
	return edges[i-1].level
}

// medianInterval 返回相邻边沿时间差的中位数（半周期估计）。
func medianInterval(edges []cedge) int64 {
	if len(edges) < 2 {
		return 0
	}
	d := make([]int64, 0, len(edges)-1)
	for i := 1; i < len(edges); i++ {
		if dt := edges[i].t - edges[i-1].t; dt > 0 {
			d = append(d, dt)
		}
	}
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

func rises(edges []cedge) []cedge {
	out := make([]cedge, 0, len(edges))
	for _, e := range edges {
		if e.level == 1 {
			out = append(out, e)
		}
	}
	return out
}

func falls(edges []cedge) []cedge {
	out := make([]cedge, 0, len(edges))
	for _, e := range edges {
		if e.level == 0 {
			out = append(out, e)
		}
	}
	return out
}

const defaultBudget = 200000

type candidateSpec struct {
	key     string
	label   string
	penalty float64
	txns    []model.Transaction
	events  []model.DecodeEvent
}

type decoderOutput struct {
	specs       []candidateSpec
	diags       []model.Diagnostic
	unsolvable  bool
	noTraffic   bool
}

func finalizeRun(m *model.Material, p model.DecodeParams, out decoderOutput) model.DecodeRun {
	run := model.DecodeRun{
		MaterialID:  m.ID,
		Params:      p,
		Diagnostics: out.diags,
	}
	run.ID = run.ComputeID()
	// 等价输入得到稳定顺序：先按置信度降序，再按 key 字典序。
	sort.SliceStable(out.specs, func(i, j int) bool {
		ci, cj := 1-out.specs[i].penalty, 1-out.specs[j].penalty
		if ci != cj {
			return ci > cj
		}
		return out.specs[i].key < out.specs[j].key
	})
	for _, s := range out.specs {
		cand := model.Candidate{
			ID:           model.Digest("candidate", run.ID, s.key),
			RunID:        run.ID,
			Key:          s.key,
			Label:        s.label,
			Confidence:   round3(1 - s.penalty),
			Rationale:    s.events,
			Transactions: s.txns,
		}
		for ti := range cand.Transactions {
			t := &cand.Transactions[ti]
			t.ID = model.Digest("txn", cand.ID, ti)
			for bi := range t.Bytes {
				b := &t.Bytes[bi]
				b.ID = model.Digest("byte", cand.ID, ti, b.Index)
			}
		}
		run.Candidates = append(run.Candidates, cand)
	}
	if out.unsolvable {
		run.Status = model.StatusSemanticUnsolvable
	} else {
		run.Status = model.StatusOK
	}
	return run
}

func round3(v float64) float64 {
	return float64(int(v*1000+0.5)) / 1000
}

func missingRoleDiag(roles map[string]bool, m *model.Material, p *prepared) []model.Diagnostic {
	var diags []model.Diagnostic
	for role := range roles {
		if _, ok := p.byRole[role]; !ok || len(p.byRole[role]) == 0 {
			diags = append(diags, model.Diagnostic{
				Code:      "missing_role",
				Message:   fmt.Sprintf("缺少承担 %s 角色的通道或该通道无边沿，拒绝用默认值伪装成功", role),
				EdgeIndex: -1,
				Channel:   -1,
			})
		}
	}
	return diags
}

// Decode 是服务端业务规则入口。
func Decode(m *model.Material, p model.DecodeParams) model.DecodeRun {
	prep := prepare(m)

	run := model.DecodeRun{MaterialID: m.ID, Params: p}
	run.Diagnostics = append(run.Diagnostics, prep.diags...)

	budget := p.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	if len(m.Edges) > budget {
		run.Status = model.StatusBudgetExhausted
		run.Diagnostics = append(run.Diagnostics, model.Diagnostic{
			Code:      "budget_exhausted",
			Message:   fmt.Sprintf("边沿数 %d 超过资源预算 %d，解码在派生前中止", len(m.Edges), budget),
			EdgeIndex: budget,
			Channel:   -1,
		})
		run.ID = run.ComputeID()
		return run
	}
	if prep.corrupt {
		run.Status = model.StatusFormatCorrupt
		run.ID = run.ComputeID()
		return run
	}

	var out decoderOutput
	switch p.Decoder {
	case "i2c":
		out = decodeI2C(m, p, prep)
	case "spi":
		out = decodeSPI(m, p, prep)
	case "sync":
		out = decodeSync(m, p, prep)
	default:
		run.Status = model.StatusFormatCorrupt
		run.Diagnostics = append(run.Diagnostics, model.Diagnostic{
			Code: "unknown_decoder", Message: "未知解码器: " + p.Decoder, EdgeIndex: -1, Channel: -1,
		})
		run.ID = run.ComputeID()
		return run
	}

	final := finalizeRun(m, p, out)
	final.Diagnostics = run.Diagnostics
	return final
}

// mustJSON 仅供事件 Detail 使用。
func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Package decode 把数字通道边沿译为可复核的 I2C / SPI / 自定义同步交易。
// 纯函数、确定性：等价输入得到稳定顺序与稳定身份。
package decode

import (
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/model"
)

// DecoderVersion 随结果持久化，复核时可恢复当时的规则版本。
const DecoderVersion = "edgebench-decode/1.0.0"

// 结果状态：资源预算耗尽、格式损坏、语义无解是三种不同结果。
const (
	StatusOk              = "ok"
	StatusBudgetExhausted = "budget_exhausted"
	StatusCorrupt         = "corrupt"
	StatusUnsolvable      = "unsolvable"
)

// signal 是派生层通道视图：已应用电平反相，原始材料保持不变。
type signal struct {
	alias string
	edges []model.Edge
}

func (s *signal) initial() int {
	if len(s.edges) == 0 {
		return -1
	}
	return 1 - s.edges[0].Level
}

// levelAt 返回 t 时刻（含）之前的电平。
func (s *signal) levelAt(t int64) int {
	lvl := s.initial()
	for _, e := range s.edges {
		if e.T > t {
			break
		}
		lvl = e.Level
	}
	return lvl
}

// lastEdgeAtOrBefore 返回 t 之前（含）最后一条边沿的下标，无则 -1。
func (s *signal) lastEdgeAtOrBefore(t int64) int {
	idx := -1
	for i, e := range s.edges {
		if e.T > t {
			break
		}
		idx = i
	}
	return idx
}

// Run 执行一次完整解码。输入不完整或损坏时返回带位置的诊断，
// 不会用空值或默认值伪装成功。
func Run(cap *model.Capture, ps *model.ParamSet) *model.Result {
	res := &model.Result{
		ParamVersion:   ps.Version,
		DecoderVersion: DecoderVersion,
		Status:         StatusOk,
		Parents:        []string{cap.ID, ps.ID},
	}
	res.InputDigest = model.ShortID("in", model.DigestJSON(cap), model.DigestJSON(ps))

	if diags := validateCapture(cap); len(diags) > 0 {
		res.Status = StatusCorrupt
		res.Diagnostics = diags
		res.ID = model.ShortID("res", res.InputDigest, fmt.Sprint(ps.Version), res.Status)
		return res
	}

	sigs := map[string]*signal{}
	for _, ch := range cap.Channels {
		edges := make([]model.Edge, len(ch.Edges))
		for i, e := range ch.Edges {
			lvl := e.Level
			if ch.Inverted {
				lvl = 1 - lvl
			}
			edges[i] = model.Edge{T: e.T, Level: lvl}
		}
		sigs[ch.Alias] = &signal{alias: ch.Alias, edges: edges}
	}

	for i := range ps.Buses {
		bus := &ps.Buses[i]
		switch bus.Protocol {
		case "i2c":
			decodeI2C(bus, sigs, res)
		case "spi":
			decodeSPI(bus, sigs, res)
		case "sync":
			decodeSync(bus, sigs, res)
		default:
			res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
				Code: "unsolvable", Bus: bus.Name,
				Message: fmt.Sprintf("bus %s: unknown protocol %q", bus.Name, bus.Protocol),
			})
		}
	}

	sort.SliceStable(res.Transactions, func(i, j int) bool {
		a, b := res.Transactions[i], res.Transactions[j]
		if a.StartNs != b.StartNs {
			return a.StartNs < b.StartNs
		}
		if a.Bus != b.Bus {
			return a.Bus < b.Bus
		}
		return a.ID < b.ID
	})
	sort.SliceStable(res.Diagnostics, func(i, j int) bool {
		if res.Diagnostics[i].TimeNs != res.Diagnostics[j].TimeNs {
			return res.Diagnostics[i].TimeNs < res.Diagnostics[j].TimeNs
		}
		return res.Diagnostics[i].Code < res.Diagnostics[j].Code
	})
	res.Status = worstStatus(res)
	res.ID = model.ShortID("res", res.InputDigest, fmt.Sprint(ps.Version), res.Status,
		fmt.Sprint(len(res.Transactions)))
	return res
}

func worstStatus(res *model.Result) string {
	status := StatusOk
	bump := func(s string) {
		switch s {
		case StatusCorrupt:
			status = StatusCorrupt
		case StatusUnsolvable:
			if status != StatusCorrupt {
				status = StatusUnsolvable
			}
		case StatusBudgetExhausted:
			if status != StatusCorrupt && status != StatusUnsolvable {
				status = StatusBudgetExhausted
			}
		}
	}
	for _, d := range res.Diagnostics {
		switch d.Code {
		case "corrupt":
			bump(StatusCorrupt)
		case "unsolvable":
			bump(StatusUnsolvable)
		case "budget_exhausted":
			bump(StatusBudgetExhausted)
		}
	}
	for _, txn := range res.Transactions {
		for _, d := range txn.Diagnostics {
			if d.Code == "budget_exhausted" {
				bump(StatusBudgetExhausted)
			}
		}
	}
	return status
}

// validateCapture 检查格式损坏；非法值留在原位置并附诊断，不做静默转换。
func validateCapture(cap *model.Capture) []model.Diagnostic {
	var diags []model.Diagnostic
	if cap.TimeUnit == "" {
		diags = append(diags, model.Diagnostic{
			Code: "corrupt", Message: "capture missing time_unit (原始表示缺少时间基准)"})
	}
	if cap.SampleRateHz <= 0 {
		diags = append(diags, model.Diagnostic{
			Code: "corrupt", Message: "capture missing/invalid sample_rate_hz"})
	}
	for _, ch := range cap.Channels {
		prev := int64(-1) << 62
		for i, e := range ch.Edges {
			if e.Level != 0 && e.Level != 1 {
				diags = append(diags, model.Diagnostic{
					Code: "corrupt", Channel: ch.Alias, EdgeIdx: i, TimeNs: e.T,
					Message: fmt.Sprintf("channel %s edge %d has invalid level %d (值留在原位置，未转换)",
						ch.Alias, i, e.Level)})
			}
			if e.T < prev {
				diags = append(diags, model.Diagnostic{
					Code: "corrupt", Channel: ch.Alias, EdgeIdx: i, TimeNs: e.T,
					Message: fmt.Sprintf("channel %s edge %d non-monotonic time %d < %d",
						ch.Alias, i, e.T, prev)})
			}
			prev = e.T
		}
	}
	return diags
}

func needChan(res *model.Result, bus *model.BusSpec, sigs map[string]*signal, alias, role string) *signal {
	s, ok := sigs[alias]
	if !ok || len(s.edges) == 0 {
		res.Diagnostics = append(res.Diagnostics, model.Diagnostic{
			Code: "unsolvable", Bus: bus.Name, Channel: alias,
			Message: fmt.Sprintf("bus %s: channel alias %q (%s) missing or has no edges; 无法唯一决定，未用默认值顶替",
				bus.Name, alias, role)})
		return nil
	}
	return s
}

func budgetOf(bus *model.BusSpec) int {
	if bus.Budget > 0 {
		return bus.Budget
	}
	return 8
}

// bitV 是一个待裁决的位：可能因缺边/边界采样而歧义。
type bitV struct {
	v     int
	ambig bool
	t     int64
	ref   model.EdgeRef
}

// forkCandidates 把歧义位展开为多个候选（受预算限制）。
// 返回候选字节序列列表与是否耗尽预算。
func forkCandidates(bits []bitV, wordBits int, budget int, mkValue func(bits []bitV) int) ([][]bitV, bool) {
	amb := 0
	for _, b := range bits {
		if b.ambig {
			amb++
		}
	}
	combos := 1
	for i := 0; i < amb; i++ {
		combos *= 2
	}
	exhausted := false
	if combos > budget {
		combos = budget
		exhausted = true
	}
	out := make([][]bitV, 0, combos)
	for c := 0; c < combos; c++ {
		nb := make([]bitV, len(bits))
		copy(nb, bits)
		k := c
		for i := range nb {
			if nb[i].ambig {
				nb[i].v = k & 1
				nb[i].ambig = false
				k >>= 1
			}
		}
		out = append(out, nb)
	}
	return out, exhausted
}

func bitsValue(bits []bitV) int {
	v := 0
	for _, b := range bits {
		v = v<<1 | (b.v & 1)
	}
	return v
}

func sortCandidates(cands []model.Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Decoder != b.Decoder {
			return a.Decoder < b.Decoder
		}
		return a.ID < b.ID
	})
}

func confOf(anomalies int) float64 {
	c := 1.0 - 0.15*float64(anomalies)
	if c < 0.05 {
		c = 0.05
	}
	return float64(int(c*100+0.5)) / 100
}

func paramKey(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(m[k])
		sb.WriteString(";")
	}
	return sb.String()
}

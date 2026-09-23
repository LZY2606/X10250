package core

import (
	"fmt"
	"sort"
	"strings"
)

// byteOpt 一个字节位置上的候选选项。
type byteOpt struct {
	cand   ByteCand
	tag    string // 空串表示默认项
	weight float64
}

// buildVariants 把逐字节候选做笛卡尔积，受预算约束。
// 置信度按权重归一；标签由非空选项标签组成，保证稳定顺序。
func buildVariants(txID string, opts [][]byteOpt, budget *Budget) ([]Variant, bool) {
	type acc struct {
		tags   []string
		bytes  []ByteCand
		weight float64
	}
	cur := []acc{{weight: 1}}
	blown := false
	for _, optsAtByte := range opts {
		var next []acc
		for _, a := range cur {
			for _, o := range optsAtByte {
				na := acc{
					tags:   append(append([]string{}, a.tags...), o.tag),
					bytes:  append(append([]ByteCand{}, a.bytes...), o.cand),
					weight: a.weight * o.weight,
				}
				next = append(next, na)
			}
		}
		if len(next) > len(cur) && !budget.Take(len(next) - len(cur)) {
			blown = true
			break
		}
		cur = next
	}
	// 稳定顺序：权重降序，标签升序。
	sort.SliceStable(cur, func(i, j int) bool {
		if cur[i].weight != cur[j].weight {
			return cur[i].weight > cur[j].weight
		}
		return strings.Join(cur[i].tags, "+") < strings.Join(cur[j].tags, "+")
	})
	var sum float64
	for _, a := range cur {
		sum += a.weight
	}
	if sum == 0 {
		sum = 1
	}
	variants := make([]Variant, 0, len(cur))
	for _, a := range cur {
		var tags []string
		for _, t := range a.tags {
			if t != "" {
				tags = append(tags, t)
			}
		}
		label := "base"
		if len(tags) > 0 {
			label = strings.Join(tags, "+")
		}
		vid := txID + ":V:" + label
		for i := range a.bytes {
			a.bytes[i].ID = fmt.Sprintf("%s:B%d", vid, a.bytes[i].Index)
			if a.bytes[i].Lane != "" {
				a.bytes[i].ID = fmt.Sprintf("%s:%s%d", vid, a.bytes[i].Lane, a.bytes[i].Index)
			}
		}
		variants = append(variants, Variant{
			ID: vid, Label: label, Confidence: a.weight / sum, Bytes: a.bytes,
		})
	}
	return variants, blown
}

// DecodeAll 对整份捕获运行全部协议译码器，返回一次 Run。
// 反相在派生层应用；原始边沿保持不变。
func DecodeAll(c *Capture, p Params, maxCandidates int) *Run {
	budget := &Budget{Max: maxCandidates}
	roleEdges := map[string][]Edge{}
	csByDevice := map[string][]Edge{}
	for _, e := range c.Edges {
		if !e.Valid {
			continue
		}
		ch, ok := c.ChannelByName(e.Ch)
		if !ok {
			continue
		}
		lv := e.Level
		if ch.Invert {
			lv = 1 - lv
		}
		ne := e
		ne.Level = lv
		ne.Ch = ch.Role
		roleEdges[ch.Role] = append(roleEdges[ch.Role], ne)
		if ch.Role == "CS" {
			dev := ch.Device
			if dev == "" {
				dev = ch.Alias
			}
			csByDevice[dev] = append(csByDevice[dev], ne)
		}
	}
	run := &Run{
		ParamsVersion:  p.Version,
		DecoderVersion: DecoderVersion,
		InputDigest:    c.Digest(),
	}
	var txs []Transaction
	if len(roleEdges["SDA"]) > 0 || len(roleEdges["SCL"]) > 0 {
		t, d := DecodeI2C(append(roleEdges["SDA"], roleEdges["SCL"]...), p.I2C, budget)
		txs = append(txs, t...)
		run.Diagnostics = append(run.Diagnostics, d...)
	}
	if len(roleEdges["SCK"]) > 0 {
		t, d := DecodeSPI(roleEdges["SCK"], roleEdges["MOSI"], roleEdges["MISO"], csByDevice, p.SPI, budget)
		txs = append(txs, t...)
		run.Diagnostics = append(run.Diagnostics, d...)
	}
	if len(roleEdges["CLK"]) > 0 {
		t, d := DecodeSync(roleEdges["CLK"], roleEdges["DATA"], p.Sync, budget)
		txs = append(txs, t...)
		run.Diagnostics = append(run.Diagnostics, d...)
	}
	sort.SliceStable(txs, func(i, j int) bool {
		if txs[i].StartPS != txs[j].StartPS {
			return txs[i].StartPS < txs[j].StartPS
		}
		return txs[i].ID < txs[j].ID
	})
	run.Transactions = txs
	run.Status = runStatus(txs, run.Diagnostics, len(c.Edges) > 0)
	run.ID = fmt.Sprintf("R%d-%s", p.Version, run.InputDigest[:8])
	return run
}

func runStatus(txs []Transaction, diags []Diagnostic, hasEdges bool) Status {
	if len(txs) == 0 {
		if hasEdges {
			return StatusUnsolvable
		}
		return StatusOK
	}
	status := StatusOK
	for _, t := range txs {
		switch t.Status {
		case StatusBudgetExhausted:
			return StatusBudgetExhausted
		case StatusAmbiguous:
			status = StatusAmbiguous
		case StatusUnsolvable:
			if status == StatusOK {
				status = StatusAmbiguous
			}
		}
	}
	for _, d := range diags {
		if d.Code == "budget_exhausted" {
			return StatusBudgetExhausted
		}
	}
	return status
}

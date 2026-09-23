package casepack

import (
	"fmt"

	"edgebench/internal/model"
	"edgebench/internal/store"
)

// buildArchives 固化反例档案：每条目只改动一个直接来源，保留采纳与未采纳结果，
// 并把机器可读取的判定依据事件一并存档。
func buildArchives(st *store.Store, i2c, spiV1, spiV2, syncV1, syncV2 model.DecodeRun, mode0ID, mode1ID string) error {
	base := func(r model.DecodeRun) *model.Candidate { return &r.Candidates[0] }

	add := func(title, changed string, baseline, variant model.DecodeRun, scope []store.AffectedObject,
		adopted, notAdopted []string, evs []model.DecodeEvent) error {
		return st.SaveArchive(store.ArchiveEntry{
			Title: title, BaselineRunID: baseline.ID, ChangedSource: changed,
			VariantRunID: variant.ID, AffectedScope: scope,
			Adopted: adopted, NotAdopted: notAdopted, MachineEvents: evs,
		}, nil)
	}

	// 0. 基线固化：原始表示六要素全保留。
	i2cBase, spiBase, syncBase := base(i2c), base(spiV1), base(syncV1)
	if err := add("基线：原边沿时间/采样率/通道别名/电平反相/时钟极性/解码器版本全部保留",
		"none（基线，不改动任何来源）", i2c, i2c, nil,
		[]string{i2cBase.ID, spiBase.ID, syncBase.ID},
		nonBaseIDs(i2c, 0, spiV1, 0, syncV1, 0), nil); err != nil {
		return err
	}

	// 1. 时钟极性（参数修订 v1→v2）：只影响同步总线字值。
	scope, evs := diffScope(syncV1, syncV2, "clock_polarity")
	if err := add("时钟极性修订：rising→falling 只重排同步总线采样沿",
		"params.clock_polarity (v1 rising → v2 falling)", syncV1, syncV2, scope,
		[]string{base(syncV2).ID}, []string{base(syncV1).ID}, evs); err != nil {
		return err
	}

	// 2. 边界采样：SPI 数据沿距采样沿 ≤ epsilon，保留前后两个采样归属。
	if evs, scope := eventScope(spiV1, "boundary_sample"); len(evs) > 0 {
		var variantID string
		for _, c := range spiV1.Candidates {
			if contains(c.Key, "boundary_sample") {
				variantID = c.ID
			}
		}
		if err := add("边界采样：数据沿与时钟沿距离进入 epsilon，保留两个归属候选",
			"sample_point.attribution (sample_after → sample_before，仅翻转一个边界点)",
			spiV1, spiV1, scope, []string{spiBase.ID}, orEmpty(variantID), evs); err != nil {
			return err
		}
	}

	// 3. 重复 START：无 STOP 的二次 START 分成两段。
	if evs, scope := eventScope(i2c, "repeated_start"); len(evs) > 0 {
		if err := add("重复 START：START 出现在未结束帧内，分段不合并",
			"i2c.condition (在已有帧内再次出现 START，仅这一条件)",
			i2c, i2c, scope, []string{i2cBase.ID}, nonBaseIDs(i2c, 0), evs); err != nil {
			return err
		}
	}

	// 4. clock stretching：从机保持 SCL 低，交易继续但留证。
	evsI, scopeI := eventScope(i2c, "clock_stretch")
	evsS, scopeS := eventScope(spiV1, "clock_stretch")
	if err := add("clock stretching：低电平超长，继续采样并记录区间",
		"scl.low_duration (单段超长，其它时序不变)",
		i2c, i2c, append(scopeI, scopeS...), []string{i2cBase.ID}, nonBaseIDs(i2c, 0), append(evsI, evsS...)); err != nil {
		return err
	}

	// 5. 数据缺口：缺口两侧不拼接成交易。
	evsI, scopeI = eventScope(i2c, "data_gap")
	evsS, scopeS = eventScope(spiV1, "data_gap")
	if err := add("数据缺口：时钟沉默超过 8 倍周期，缺口两侧不拼接",
		"scl.silence_duration (单段沉默，交易边界策略不变)",
		i2c, i2c, append(scopeI, scopeS...), []string{i2cBase.ID, spiBase.ID},
		append(nonBaseIDs(i2c, 0), nonBaseIDs(spiV1, 0)...), append(evsI, evsS...)); err != nil {
		return err
	}

	// 6. 多设备片选：每片选窗口独立分组。
	if evs, scope := eventScope(spiV1, "multi_cs_select"); len(evs) > 0 {
		var csObjs []store.AffectedObject
		for _, t := range spiBase.Transactions {
			csObjs = append(csObjs, store.AffectedObject{Kind: "transaction", ID: t.ID,
				Note: fmt.Sprintf("CS_DEV%d 窗口", t.CS)})
		}
		scope = append(scope, csObjs...)
		if err := add("多设备片选：CS_DEV0/CS_DEV1 的传输分组保留",
			"cs.windows (片选窗口数=2，其余波形不变)",
			spiV1, spiV1, scope, []string{spiBase.ID}, nonBaseIDs(spiV1, 0), evs); err != nil {
			return err
		}
	}

	// 7. 并列候选：缺边与 SPI mode 不明时保留全部合法候选。
	evsI, scopeI = eventScope(i2c, "missing_clock_edge")
	evsS, scopeS = eventScope(spiV1, "spi_mode_ambiguous", "parallel_candidates_kept")
	evsI = append(evsI, evsS...)
	scopeI = append(scopeI, scopeS...)
	if err := add("并列候选：时钟缺边 / SPI mode 不明时全部合法候选并存",
		"ambiguity_policy (单点翻转，默认插入虚拟位 ↔ 结束传输)",
		i2c, spiV1, scopeI,
		[]string{i2cBase.ID, mode0ID},
		append(nonBaseIDs(i2c, 0), nonBaseIDs(spiV1, 0)...),
		evsI); err != nil {
		return err
	}

	// 8. 局部源数据改动：SPI 追加一笔，受影响范围仅限 SPI 派生。
	scope, evs = diffScope(spiV1, spiV2, "source_edges_appended")
	if err := add("局部源数据改动：CS_DEV0 追加 0x42，I2C/同步依赖不受影响",
		"material.edges[spi-rev-a] (仅追加，原边沿不改写)", spiV1, spiV2, scope,
		[]string{spiV2.Candidates[0].ID}, []string{spiBase.ID}, evs); err != nil {
		return err
	}

	// 9. 互相冲突的人工决定：采纳与否定并存，显式标冲突，不覆盖。
	if err := add("人工决定冲突：alice 采纳 mode0，bob 否定 mode0 并采纳 mode1",
		"decision.reviewer (两次决定针对同一候选，均保留)", spiV1, spiV1,
		[]store.AffectedObject{{Kind: "candidate", ID: mode0ID, Note: "adopt(alice) 与 reject(bob) 冲突"},
			{Kind: "candidate", ID: mode1ID, Note: "adopt(bob)"}},
		[]string{mode0ID, mode1ID}, nil,
		[]model.DecodeEvent{{Type: "conflicting_decisions", EdgeIndex: -1,
			Detail: fmt.Sprintf(`{"adopt":"%s","reject":"%s","adopt_other":"%s"}`, mode0ID, mode0ID, mode1ID)}}); err != nil {
		return err
	}
	return nil
}

// eventScope 收集指定类型的机器事件，并把它们定位到所属交易作为受影响对象。
func eventScope(r model.DecodeRun, types ...string) ([]model.DecodeEvent, []store.AffectedObject) {
	want := map[string]bool{}
	for _, t := range types {
		want[t] = true
	}
	var evs []model.DecodeEvent
	seen := map[string]bool{}
	var scope []store.AffectedObject
	cand := r.Candidates[0]
	for _, e := range cand.Rationale {
		if !want[e.Type] {
			continue
		}
		evs = append(evs, e)
		for _, t := range cand.Transactions {
			if seen[t.ID] || e.TimeNs < t.StartNs || e.TimeNs > t.EndNs {
				continue
			}
			seen[t.ID] = true
			scope = append(scope, store.AffectedObject{Kind: "transaction", ID: t.ID, Note: e.Type})
		}
		if !want[e.Type] {
			continue
		}
		if e.EdgeIndex < 0 {
			scope = append(scope, store.AffectedObject{Kind: "candidate", ID: cand.ID, Note: e.Type})
		}
	}
	return evs, scope
}

// diffScope 比较两个 run 基线候选的字节差异，输出受影响字节范围与缺口事件。
func diffScope(old, new model.DecodeRun, change string) ([]store.AffectedObject, []model.DecodeEvent) {
	var scope []store.AffectedObject
	c1, c2 := old.Candidates[0], new.Candidates[0]
	n := len(c1.Transactions)
	if len(c2.Transactions) > n {
		n = len(c2.Transactions)
	}
	for i := 0; i < n; i++ {
		if i >= len(c1.Transactions) {
			for _, b := range c2.Transactions[i].Bytes {
				scope = append(scope, store.AffectedObject{Kind: "byte", ID: b.ID, Note: "新增交易字节"})
			}
			scope = append(scope, store.AffectedObject{Kind: "transaction", ID: c2.Transactions[i].ID, Note: change})
			continue
		}
		if i >= len(c2.Transactions) {
			for _, b := range c1.Transactions[i].Bytes {
				scope = append(scope, store.AffectedObject{Kind: "byte", ID: b.ID, Note: "改动后缺失"})
			}
			continue
		}
		t1, t2 := c1.Transactions[i], c2.Transactions[i]
		if len(t1.Bytes) != len(t2.Bytes) {
			scope = append(scope, store.AffectedObject{Kind: "transaction", ID: t2.ID, Note: change})
			continue
		}
		for j := range t1.Bytes {
			if t1.Bytes[j].Value != t2.Bytes[j].Value || t1.Bytes[j].Ack != t2.Bytes[j].Ack {
				scope = append(scope, store.AffectedObject{Kind: "byte", ID: t2.Bytes[j].ID,
					Note: fmt.Sprintf("%s: 0x%02X→0x%02X", change, t1.Bytes[j].Value, t2.Bytes[j].Value)})
			}
		}
	}
	var evs []model.DecodeEvent
	evs = append(evs, c2.Rationale...)
	return scope, evs
}

func nonBaseIDs(rs ...any) []string {
	var out []string
	for i := 0; i < len(rs); i += 2 {
		r := rs[i].(model.DecodeRun)
		baseIdx := rs[i+1].(int)
		for j := range r.Candidates {
			if j != baseIdx {
				out = append(out, r.Candidates[j].ID)
			}
		}
	}
	return out
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func orEmpty(id string) []string {
	if id == "" {
		return nil
	}
	return []string{id}
}

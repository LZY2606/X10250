package casepack

import (
	"fmt"
	"time"

	"edgebench/internal/decode"
	"edgebench/internal/model"
	"edgebench/internal/store"
)

// SeedResult 记录重放后各对象身份，供测试与界面引用。
type SeedResult struct {
	I2CMaterial, SPIMaterialV1, SPIMaterialV2, SyncMaterial string
	I2CRun, SPIRunV1, SPIRunV2, SyncRunV1, SyncRunV2        string
	AliceDecision, BobReject, BobAdopt                      string
}

type materialPayload struct {
	MaterialID    string   `json:"material_id"`
	Name          string   `json:"name"`
	EdgeCount     int      `json:"edge_count"`
	SampleRateHz  int64    `json:"sample_rate_hz"`
	ClockPolarity string   `json:"clock_polarity"`
	Aliases       []string `json:"channel_aliases"`
	Inverted      []int    `json:"inverted_channels"`
	Source        string   `json:"source"`
}

type runPayload struct {
	RunID        string             `json:"run_id"`
	MaterialID   string             `json:"material_id"`
	Params       model.DecodeParams `json:"params"`
	Status       string             `json:"status"`
	CandidateIDs []string           `json:"candidate_ids"`
}

// Seed 在空存储上完整重放“边沿译码台”叙事。它与 HTTP 路径使用同一套服务端规则。
func Seed(st *store.Store) (SeedResult, error) {
	var res SeedResult
	clock := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC).UnixNano()
	step := 0
	now := func() int64 { step++; return clock + int64(step)*1_000_000 }

	importMaterial := func(m model.Material) (model.Material, string, error) {
		m.ID = m.ComputeID()
		m.CreatedAt = now()
		if err := st.SaveMaterial(m, nil); err != nil {
			return m, "", err
		}
		aliases := make([]string, len(m.Probe.Channels))
		var inv []int
		for i, c := range m.Probe.Channels {
			aliases[i] = fmt.Sprintf("%d:%s(%s,inverted=%v)", c.Index, c.Alias, c.Role, c.Inverted)
			if c.Inverted {
				inv = append(inv, c.Index)
			}
		}
		ev, err := st.AppendEvent("import_material", materialPayload{
			MaterialID: m.ID, Name: m.Name, EdgeCount: len(m.Edges),
			SampleRateHz: m.Probe.SampleRateHz, ClockPolarity: m.Probe.ClockPolarity,
			Aliases: aliases, Inverted: inv, Source: m.Source,
		}, nil, nil)
		return m, ev.ID, err
	}

	decodeAndStore := func(m model.Material, p model.DecodeParams, parentEvent string) (model.DecodeRun, string, error) {
		run := decode.Decode(&m, p)
		ev, err := st.AppendEvent("decode_run", runPayload{
			RunID: run.ID, MaterialID: m.ID, Params: p, Status: run.Status,
			CandidateIDs: candIDs(run),
		}, []string{parentEvent}, nil)
		if err != nil {
			return run, "", err
		}
		run.CreatedSeq = ev.Seq
		if err := st.SaveRun(run, nil); err != nil {
			return run, "", err
		}
		return run, ev.ID, nil
	}

	// 1. 初始导入：原始边沿时间、采样率、别名、电平反相、时钟极性全部留在材料层。
	i2cMat := BuildI2C()
	spiMat := BuildSPI(false)
	syncMat := BuildSync()
	var i2cImportEv, spiImportEv, syncImportEv string
	var err error
	if i2cMat, i2cImportEv, err = importMaterial(i2cMat); err != nil {
		return res, err
	}
	if spiMat, spiImportEv, err = importMaterial(spiMat); err != nil {
		return res, err
	}
	if syncMat, syncImportEv, err = importMaterial(syncMat); err != nil {
		return res, err
	}
	res.I2CMaterial, res.SPIMaterialV1, res.SyncMaterial = i2cMat.ID, spiMat.ID, syncMat.ID

	// 2. 基线解码（解码器版本固化在参数中）。
	i2cP := model.DecodeParams{Decoder: "i2c", DecoderVersion: "i2c/1.2.0", ParamVersion: 1, GapFactor: 8, StretchFactor: 3}
	spiP := model.DecodeParams{Decoder: "spi", DecoderVersion: "spi/2.0.1", ParamVersion: 1, GapFactor: 8, StretchFactor: 3, SPIMode: "unknown"}
	syncP := model.DecodeParams{Decoder: "sync", DecoderVersion: "sync/0.9.3", ParamVersion: 1, GapFactor: 8, StretchFactor: 3}

	var i2cRun, spiRunV1, syncRunV1 model.DecodeRun
	var spiRunEv, syncRunEv string
	if i2cRun, _, err = decodeAndStore(i2cMat, i2cP, i2cImportEv); err != nil {
		return res, err
	}
	if spiRunV1, spiRunEv, err = decodeAndStore(spiMat, spiP, spiImportEv); err != nil {
		return res, err
	}
	if syncRunV1, syncRunEv, err = decodeAndStore(syncMat, syncP, syncImportEv); err != nil {
		return res, err
	}
	res.I2CRun, res.SPIRunV1, res.SyncRunV1 = i2cRun.ID, spiRunV1.ID, syncRunV1.ID

	// 3. 参数修订：时钟极性翻转为 falling（v2），只影响同步总线派生结果。
	syncPv2 := syncP
	syncPv2.ParamVersion = 2
	syncPv2.Polarity = "falling"
	revEv, err := st.AppendEvent("revise_params", map[string]any{
		"decoder": "sync", "from_param_version": 1, "to_param_version": 2,
		"change": "clock_polarity: rising -> falling",
	}, []string{syncRunEv}, nil)
	if err != nil {
		return res, err
	}
	var syncRunV2 model.DecodeRun
	if syncRunV2, _, err = decodeAndStore(syncMat, syncPv2, revEv.ID); err != nil {
		return res, err
	}
	res.SyncRunV2 = syncRunV2.ID

	// 4. 只影响局部依赖的源数据改动：SPI 捕获追加 CS0 一笔，同步/I2C 依赖不变。
	spiMatV2 := BuildSPI(true)
	spiMatV2.Name = "spi-rev-a"
	if spiMatV2, _, err = importMaterial(spiMatV2); err != nil {
		return res, err
	}
	res.SPIMaterialV2 = spiMatV2.ID
	revMatEv, err := st.AppendEvent("revise_material", map[string]any{
		"name": "spi-rev-a", "old_material_id": spiMat.ID, "new_material_id": spiMatV2.ID,
		"change": "append CS_DEV0 transfer 0x42; i2c/sync 依赖不受影响",
	}, []string{spiRunEv}, nil)
	if err != nil {
		return res, err
	}
	var spiRunV2 model.DecodeRun
	if spiRunV2, _, err = decodeAndStore(spiMatV2, spiP, revMatEv.ID); err != nil {
		return res, err
	}
	res.SPIRunV2 = spiRunV2.ID

	// 5. 互相冲突的人工决定：alice 采纳 mode0 基线，bob 否定它并采纳 mode1。
	mode0 := findCand(spiRunV1, "mode=0;base")
	mode1 := findCandByPrefix(spiRunV1, "mode=1;base")
	if mode0 == nil || mode1 == nil {
		return res, fmt.Errorf("casepack: 缺少预期的 SPI mode 候选")
	}
	a, err := st.AppendEvent("decide", model.Decision{
		RunID: spiRunV1.ID, CandidateID: mode0.ID, Action: "adopt",
		Reviewer: "alice", Note: "波形与 CPOL=0/CPHA=0 一致，采纳 mode0",
	}, []string{spiRunEv}, nil)
	if err != nil {
		return res, err
	}
	res.AliceDecision = a.ID
	br, err := st.AppendEvent("decide", model.Decision{
		RunID: spiRunV1.ID, CandidateID: mode0.ID, Action: "reject",
		Reviewer: "bob", Note: "边界采样处读值不可靠，拒绝 mode0 基线",
	}, []string{a.ID}, nil)
	if err != nil {
		return res, err
	}
	res.BobReject = br.ID
	ba, err := st.AppendEvent("decide", model.Decision{
		RunID: spiRunV1.ID, CandidateID: mode1.ID, Action: "adopt",
		Reviewer: "bob", Note: "改采纳 mode1 并列候选",
	}, []string{br.ID}, nil)
	if err != nil {
		return res, err
	}
	res.BobAdopt = ba.ID

	// 6. 反例档案：基线 + 七类边界 + 局部改动 + 冲突决定，逐条只改一个直接来源。
	if err := buildArchives(st, i2cRun, spiRunV1, spiRunV2, syncRunV1, syncRunV2, mode0.ID, mode1.ID); err != nil {
		return res, err
	}
	return res, nil
}

func candIDs(r model.DecodeRun) []string {
	out := make([]string, len(r.Candidates))
	for i, c := range r.Candidates {
		out[i] = c.ID
	}
	return out
}

func findCand(r model.DecodeRun, key string) *model.Candidate {
	for i := range r.Candidates {
		if r.Candidates[i].Key == key {
			return &r.Candidates[i]
		}
	}
	return nil
}

func findCandByPrefix(r model.DecodeRun, prefix string) *model.Candidate {
	for i := range r.Candidates {
		if hasPrefix(r.Candidates[i].Key, prefix) {
			return &r.Candidates[i]
		}
	}
	return nil
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

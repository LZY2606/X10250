// Package casepack 自带离线领域案例包：“边沿译码台：逻辑分析波形、
// I2C/SPI 交易与重叠候选”。空库启动时载入，完整呈现导入→推导→复核叙事。
package casepack

import (
	"fmt"

	"edgebench/internal/decode"
	"edgebench/internal/model"
	"edgebench/internal/siggen"
	"edgebench/internal/store"
)

// buildCapture 构造采集；fixMissingClock=false 时 I2C 第三笔交易缺一个 SCL 脉冲。
func buildCapture(fixMissingClock bool) *model.Capture {
	// --- I2C：200 kHz，含重复 START、clock stretching、时钟缺边、数据缺口 ---
	i2c := siggen.NewI2C(2500, 1000)
	i2c.Start()
	i2c.Byte(0xA0, true) // 地址 0x50 写
	i2c.Byte(0x5A, true)
	i2c.Stop()
	i2c.Wait(30000) // 数据缺口
	i2c.Start()
	i2c.Byte(0xA1, true)                        // 地址 0x50 读
	i2c.StretchByte(0x3C, true, 2, 12000)       // clock stretching
	i2c.RepeatedStart()                         // 重复 START
	i2c.Byte(0xA0, true)                        // 地址 0x50 写
	if fixMissingClock {
		i2c.Byte(0x77, true)
	} else {
		i2c.MissingClockByte(0x77, true, 3) // 时钟缺边
	}
	i2c.Stop()
	i2c.Wait(40000) // 交易间空闲
	i2c.Start()
	i2c.Byte(0xB0, true)
	i2c.Wait(30000) // 交易内数据缺口：截断，不拼接
	i2c.Stop()
	i2c.Start()
	i2c.Byte(0xB2, true)
	i2c.Stop()
	i2c.Wait(40000)
	i2c.Start()
	i2c.Byte(0xA2, false) // 地址 0x51 写，NACK
	i2c.Stop()

	// --- SPI：mode 0 信号，但参数不明；多设备片选；CS1 上有边界采样 ---
	spi := siggen.NewSPI(1000, 500, 0)
	spi.CSActive("CS0")
	spi.Byte(0x9F, 0x00)
	spi.Byte(0x18, 0x42)
	spi.CSIdle("CS0")
	spi.Wait(20000)
	spi.CSActive("CS1")
	spi.ByteBoundary(0x55, 0xC3, 2) // 边界采样
	spi.CSIdle("CS1")

	// --- 自定义同步：极性不明，两段字之间数据缺口 ---
	sync := siggen.NewSync(800, 2000)
	sync.Word(0xB2, 8)
	sync.Word(0x69, 8)
	sync.Wait(30000) // 数据缺口
	sync.Word(0xFF, 8)

	// CS0 原始记录做电平反相（探头配置），派生层再规范回来。
	cs0raw := make([]model.Edge, len(spi.CS["CS0"]))
	for i, e := range spi.CS["CS0"] {
		cs0raw[i] = model.Edge{T: e.T, Level: 1 - e.Level}
	}

	return &model.Capture{
		ID:           "capture-logic-001",
		TimeUnit:     model.TimeUnitNS,
		SampleRateHz: 100_000_000,
		Note:         "逻辑分析采集：I2C + SPI(双片选) + 自定义同步",
		Channels: []model.Channel{
			{Name: "D0", Alias: "SDA", Edges: i2c.SDA},
			{Name: "D1", Alias: "SCL", Edges: i2c.SCL},
			{Name: "D2", Alias: "SCLK", Edges: spi.CLK},
			{Name: "D3", Alias: "MOSI", Edges: spi.MOSI},
			{Name: "D4", Alias: "MISO", Edges: spi.MISO},
			{Name: "D5", Alias: "CS0", Inverted: true, Edges: cs0raw},
			{Name: "D6", Alias: "CS1", Edges: spi.CS["CS1"]},
			{Name: "D7", Alias: "SYNC_CLK", Edges: sync.CLK},
			{Name: "D8", Alias: "SYNC_DATA", Edges: sync.DATA},
		},
	}
}

func paramsV1() *model.ParamSet {
	return &model.ParamSet{
		ID: "params-v1", Version: 1, DecoderVersion: decode.DecoderVersion,
		Buses: []model.BusSpec{
			{Name: "i2c-0", Protocol: "i2c", Clock: "SCL", Data: "SDA",
				GapThresholdNs: 20000, StretchThresholdNs: 8000,
				BoundaryEpsilonNs: 40, Budget: 8, BitOrder: "msb"},
			{Name: "spi-0", Protocol: "spi", Clock: "SCLK", Data: "MOSI", Data2: "MISO",
				ChipSelects: []string{"CS0", "CS1"}, CSActive: "low", SPIMode: "unknown",
				GapThresholdNs: 20000, BoundaryEpsilonNs: 40, Budget: 8},
			{Name: "sync-0", Protocol: "sync", Clock: "SYNC_CLK", Data: "SYNC_DATA",
				ClockPolarity: "unknown", BitsPerWord: 8,
				GapThresholdNs: 20000, BoundaryEpsilonNs: 40, Budget: 8},
		},
	}
}

func paramsV2() *model.ParamSet {
	p := paramsV1()
	p.ID = "params-v2"
	p.Version = 2
	p.Buses[1].SPIMode = "0"
	p.Buses[2].ClockPolarity = "rising"
	return p
}

// Load 在空库上载入案例包叙事：基线导入 → 参数修订 → 局部源数据改动 → 冲突决定。
func Load(st *store.Store) error {
	max, err := st.MaxSeq()
	if err != nil {
		return err
	}
	if max > 0 {
		return fmt.Errorf("casepack: 库非空，拒绝覆盖载入")
	}

	// 1) 初始导入：原边沿时间、采样率、通道别名、电平反相、时钟极性、解码器版本全部保留
	capV1 := buildCapture(false)
	p1 := paramsV1()
	seq1, _, err := st.AddEvent("import_material", nil, map[string]any{
		"note": "导入数字通道边沿与探头配置（基线）：原边沿时间/采样率/通道别名/电平反相/时钟极性/解码器版本全部保留",
		"capture": capV1.ID, "params": p1.ID,
	})
	if err != nil {
		return err
	}
	capID, err := st.AddMaterial("capture", capV1, seq1)
	if err != nil {
		return err
	}
	if _, err := st.AddMaterial("params", p1, seq1); err != nil {
		return err
	}
	r1 := decode.Run(capV1, p1)
	seq2, _, err := st.AddEvent("decode_run", []int64{seq1}, map[string]any{
		"result": r1.ID, "status": r1.Status, "input_digest": r1.InputDigest,
	})
	if err != nil {
		return err
	}
	if err := st.AddResult(seq2, r1); err != nil {
		return err
	}

	// 2) 参数修订：SPI mode 定为 0、同步时钟极性定为 rising
	p2 := paramsV2()
	seq3, _, err := st.AddEvent("param_revision", []int64{seq2}, map[string]any{
		"from": 1, "to": 2,
		"changes": []string{"spi-0.spi_mode: unknown→0", "sync-0.clock_polarity: unknown→rising"},
	})
	if err != nil {
		return err
	}
	if _, err := st.AddMaterial("params", p2, seq3); err != nil {
		return err
	}
	r2 := decode.Run(capV1, p2)
	seq4, _, err := st.AddEvent("decode_run", []int64{seq3}, map[string]any{
		"result": r2.ID, "status": r2.Status, "input_digest": r2.InputDigest,
	})
	if err != nil {
		return err
	}
	if err := st.AddResult(seq4, r2); err != nil {
		return err
	}

	// 3) 局部源数据改动：示波器重导出补齐 SCL 缺失时钟边（只影响 i2c-0 依赖）
	capV2 := buildCapture(true)
	capV2.ID = "capture-logic-001a"
	capV2.Note = "重导出：补齐 SCL 缺失时钟边（局部源数据改动，父版本 " + capID + "）"
	seq5, _, err := st.AddEvent("source_change", []int64{seq4}, map[string]any{
		"scope": "i2c-0", "parent_capture": capID,
		"note": "只影响局部依赖：SPI/同步交易的输入摘要不变",
	})
	if err != nil {
		return err
	}
	if _, err := st.AddMaterial("capture", capV2, seq5); err != nil {
		return err
	}
	r3 := decode.Run(capV2, p2)
	seq6, _, err := st.AddEvent("decode_run", []int64{seq5}, map[string]any{
		"result": r3.ID, "status": r3.Status, "input_digest": r3.InputDigest,
	})
	if err != nil {
		return err
	}
	if err := st.AddResult(seq6, r3); err != nil {
		return err
	}

	// 4) 互相冲突的人工决定：两位审阅者对同一候选给出相反结论，都保留
	var spiCand string
	for _, txn := range r3.Transactions {
		if txn.Protocol == "spi" && txn.Device == "CS1" && len(txn.Candidates) > 0 {
			spiCand = txn.Candidates[0].ID
		}
	}
	if spiCand != "" {
		seq7, _, err := st.AddEvent("decision", []int64{seq6}, model.Decision{
			Target: spiCand, Action: "accept", Reviewer: "li.wei",
			Note: "边界采样位按 1 读，与 EEPROM 规格书一致",
		})
		if err != nil {
			return err
		}
		if _, _, err := st.AddEvent("decision", []int64{seq7}, model.Decision{
			Target: spiCand, Action: "reject", Reviewer: "chen.mu",
			Note: "边界窗口内采样不可信，要求提高采样率重测",
		}); err != nil {
			return err
		}
	}
	return nil
}

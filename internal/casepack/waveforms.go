// Package casepack 是“边沿译码台”离线案例包：逻辑分析波形、I2C/SPI 交易与重叠候选。
// 所有波形由代码确定生成，领域量携带时间基准（ns）与采样率（Hz）。
package casepack

import (
	"edgebench/internal/model"
)

type gen struct {
	edges []model.Edge
	t     int64
}

type line struct {
	g       *gen
	ch      int
	level   int
	started bool
}

func (g *gen) line(ch int) *line { return &line{g: g, ch: ch} }

func (l *line) set(level int) {
	if l.started && level == l.level {
		return
	}
	l.started = true
	l.level = level
	l.g.edges = append(l.g.edges, model.Edge{Channel: l.ch, TimeNs: l.g.t, Level: level})
}

func (g *gen) adv(dt int64) { g.t += dt }

// BuildI2C 生成 I2C 捕获：重复 START、clock stretching、缺边、数据缺口齐备。
// 时间基准：ns；采样率 100MHz；SCL 半周期 250ns（1MHz 总线）。
func BuildI2C() model.Material {
	g := &gen{t: 1000}
	scl, sda := g.line(0), g.line(1)
	scl.set(1)
	sda.set(1)
	g.adv(2000)
	const half = int64(250)

	start := func() {
		sda.set(0)
		g.adv(half)
		scl.set(0)
		g.adv(half)
	}
	bit := func(b, lowHalfs int) {
		sda.set(b)
		g.adv(int64(lowHalfs) * half)
		scl.set(1)
		g.adv(half)
		scl.set(0)
	}
	i2cByte := func(v, ackBit int, special map[int]int) {
		for i := 7; i >= 0; i-- {
			lh := 1
			if s, ok := special[7-i]; ok {
				lh = s
			}
			bit((v>>i)&1, lh)
		}
		bit(ackBit, 1)
	}
	stop := func() {
		sda.set(0)
		g.adv(half)
		scl.set(1)
		g.adv(half)
		sda.set(1)
		g.adv(half)
	}
	repeatedStart := func() {
		sda.set(1)
		g.adv(half)
		scl.set(1)
		g.adv(half)
		sda.set(0)
		g.adv(half)
		scl.set(0)
		g.adv(half)
	}

	// 帧 1：写地址 0xA0 + 寄存器 0x11，重复 START，读地址 0xA1，
	// 数据 0x22（第 3 位 clock stretching），0x33 以 NACK 结束，STOP。
	start()
	i2cByte(0xA0, 0, nil)
	i2cByte(0x11, 0, nil)
	repeatedStart()
	i2cByte(0xA1, 0, nil)
	i2cByte(0x22, 0, map[int]int{3: 6}) // 低电平 6 个半周期 → rise 间距 3.5 倍周期 → stretching
	i2cByte(0x33, 1, nil)               // NACK
	stop()
	g.adv(4000)

	// 帧 2：0x55 第 2 位缺一个 SCL 脉冲（缺边歧义）；0x66 第 4 位后总线沉默（数据缺口，不拼接）。
	start()
	i2cByte(0x55, 0, map[int]int{2: 3}) // rise 间距 2 倍周期 → 缺边
	i2cByte(0x66, 0, map[int]int{4: 20})
	// 缺口后主机放弃：总线回到空闲，再发起新帧。
	sda.set(1)
	g.adv(2 * half)
	scl.set(1)
	g.adv(4 * half)
	start()
	i2cByte(0x77, 0, nil)
	stop()
	g.adv(2000)

	return model.Material{
		Name:   "i2c-rev-a",
		Source: "casepack:边沿译码台/i2c 捕获 rev A",
		Edges:  g.edges,
		Probe: model.ProbeConfig{
			SampleRateHz:  100_000_000,
			ClockPolarity: "rising",
			Channels: []model.ChannelConfig{
				{Index: 0, Alias: "SCL_A", Role: "scl"},
				{Index: 1, Alias: "SDA_A", Role: "sda"},
			},
		},
	}
}

// BuildSPI 生成 SPI 捕获：双片选、缺边、数据缺口、边界采样。
// extraTail=true 时追加 CS0 的第三笔传输（局部源数据改动用例）。
func BuildSPI(extraTail bool) model.Material {
	g := &gen{t: 2000}
	clk, data := g.line(0), g.line(1)
	cs0, cs1 := g.line(2), g.line(3)
	clk.set(0)
	data.set(0)
	cs0.set(1)
	cs1.set(1)
	g.adv(2000)
	const half = int64(250)

	spiBit := func(b, lowHalfs int, boundary bool) {
		if boundary {
			// 数据沿仅提前采样沿 30ns（epsilon≈71ns）→ 边界采样歧义。
			g.adv(int64(lowHalfs)*half - 30)
			data.set(b)
			g.adv(30)
		} else {
			data.set(b)
			g.adv(int64(lowHalfs) * half)
		}
		clk.set(1)
		g.adv(half)
		clk.set(0)
	}
	spiByte := func(v int, special map[int]int, boundaryBit int) {
		for i := 7; i >= 0; i-- {
			lh := 1
			if s, ok := special[7-i]; ok {
				lh = s
			}
			spiBit((v>>i)&1, lh, 7-i == boundaryBit)
		}
	}

	// 设备 0：0x3C；0x9A（第 5 位缺时钟）；长间隙后 0x77（缺口，不拼接）。
	cs0.set(0)
	g.adv(2 * half)
	spiByte(0x3C, nil, -1)
	spiByte(0x9A, map[int]int{5: 3}, -1)
	spiByte(0x77, map[int]int{0: 20}, -1)
	cs0.set(1)
	g.adv(3000)

	// 设备 1：0xA5，第 3 位边界采样。
	cs1.set(0)
	g.adv(2 * half)
	spiByte(0xA5, nil, 3)
	cs1.set(1)
	g.adv(2000)

	if extraTail {
		// 局部源数据改动：仅向 CS0 追加一笔 0x42。
		cs0.set(0)
		g.adv(2 * half)
		spiByte(0x42, nil, -1)
		cs0.set(1)
		g.adv(2000)
	}

	return model.Material{
		Name:   "spi-rev-a",
		Source: "casepack:边沿译码台/spi 捕获 rev A",
		Edges:  g.edges,
		Probe: model.ProbeConfig{
			SampleRateHz:  100_000_000,
			ClockPolarity: "rising",
			Channels: []model.ChannelConfig{
				{Index: 0, Alias: "CLK", Role: "clk"},
				{Index: 1, Alias: "MOSI", Role: "data"},
				{Index: 2, Alias: "CS_DEV0", Role: "cs"},
				{Index: 3, Alias: "CS_DEV1", Role: "cs"},
			},
		},
	}
}

// BuildSync 生成自定义同步捕获。数据通道在材料层按探头配置反相保存，
// 派生层负责恢复，验证“电平反相保留在材料层”。
func BuildSync() model.Material {
	g := &gen{t: 1500}
	clk, data := g.line(0), g.line(1)
	clk.set(0)
	data.set(0)
	g.adv(1500)
	const half = int64(200)

	syncBit := func(b, lowHalfs int) {
		data.set(b)
		g.adv(int64(lowHalfs) * half)
		clk.set(1)
		g.adv(half)
		clk.set(0)
	}
	syncByte := func(v int, special map[int]int) {
		for i := 7; i >= 0; i-- {
			lh := 1
			if s, ok := special[7-i]; ok {
				lh = s
			}
			syncBit((v>>i)&1, lh)
		}
	}
	syncByte(0xC3, nil)
	syncByte(0x5A, nil)
	syncByte(0x0F, map[int]int{0: 18}) // 字间长间隙 → 缺口分段
	g.adv(1500)

	edges := make([]model.Edge, len(g.edges))
	copy(edges, g.edges)
	for i := range edges {
		if edges[i].Channel == 1 {
			edges[i].Level = 1 - edges[i].Level
		}
	}
	return model.Material{
		Name:   "sync-rev-a",
		Source: "casepack:边沿译码台/自定义同步捕获 rev A（数据通道反相保存）",
		Edges:  edges,
		Probe: model.ProbeConfig{
			SampleRateHz:  50_000_000,
			ClockPolarity: "rising",
			Channels: []model.ChannelConfig{
				{Index: 0, Alias: "SCLK", Role: "clk"},
				{Index: 1, Alias: "SDAT", Role: "data", Inverted: true},
			},
		},
	}
}

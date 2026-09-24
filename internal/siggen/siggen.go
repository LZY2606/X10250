// Package siggen 构造领域测试波形：I2C / SPI / 自定义同步边沿流，
// 支持注入缺时钟边、clock stretching、边界采样、重复 START、数据缺口等情形。
package siggen

import "edgebench/internal/model"

// I2C 生成器：电平域为“派生域”（未反相），时间单位 ns。
type I2C struct {
	Half     int64
	t        int64
	sda, scl int
	SDA, SCL []model.Edge
}

func NewI2C(half, startT int64) *I2C {
	return &I2C{Half: half, t: startT, sda: 1, scl: 1}
}

func (g *I2C) T() int64 { return g.t }

func (g *I2C) setSDA(v int) {
	if v != g.sda {
		g.sda = v
		g.SDA = append(g.SDA, model.Edge{T: g.t, Level: v})
	}
}

func (g *I2C) setSCL(v int) {
	if v != g.scl {
		g.scl = v
		g.SCL = append(g.SCL, model.Edge{T: g.t, Level: v})
	}
}

func (g *I2C) Wait(ns int64) { g.t += ns }

func (g *I2C) Start() {
	g.setSDA(0)
	g.Wait(g.Half)
	g.setSCL(0)
	g.Wait(g.Half)
}

func (g *I2C) RepeatedStart() {
	g.setSDA(1)
	g.Wait(g.Half)
	g.setSCL(1)
	g.Wait(g.Half)
	g.setSDA(0)
	g.Wait(g.Half)
	g.setSCL(0)
	g.Wait(g.Half)
}

func (g *I2C) bit(b int) {
	g.setSDA(b)
	g.Wait(g.Half)
	g.setSCL(1)
	g.Wait(g.Half)
	g.setSCL(0)
	g.Wait(g.Half)
}

// stretchBit 在 SCL 拉低阶段额外保持 stretchNs，模拟 clock stretching。
func (g *I2C) stretchBit(b int, stretchNs int64) {
	g.setSDA(b)
	g.Wait(g.Half)
	g.setSCL(1)
	g.Wait(g.Half)
	g.setSCL(0)
	g.Wait(g.Half + stretchNs)
}

// Byte 发送 8 位数据加 1 位 ACK（ack=true 表示从机拉低 SDA）。
func (g *I2C) Byte(v int, ack bool) {
	for i := 7; i >= 0; i-- {
		g.bit((v >> i) & 1)
	}
	g.ackBit(ack)
}

// StretchByte 在第 stretchBitIdx 个数据位（0=MSB）上拉长 SCL 低电平。
func (g *I2C) StretchByte(v int, ack bool, stretchBitIdx int, stretchNs int64) {
	for i := 7; i >= 0; i-- {
		if 7-i == stretchBitIdx {
			g.stretchBit((v>>i)&1, stretchNs)
		} else {
			g.bit((v >> i) & 1)
		}
	}
	g.ackBit(ack)
}

// MissingClockByte 在 skipBitIdx 个数据位（0=MSB）上省略 SCL 脉冲，模拟时钟缺边。
func (g *I2C) MissingClockByte(v int, ack bool, skipBitIdx int) {
	for i := 7; i >= 0; i-- {
		if 7-i == skipBitIdx {
			g.setSDA((v >> i) & 1)
			g.Wait(2 * g.Half) // 没有 SCL 脉冲
			continue
		}
		g.bit((v >> i) & 1)
	}
	g.ackBit(ack)
}

// BoundaryByte 在第 bndBitIdx 个数据位上让 SDA 跳变与 SCL 上升沿几乎同时（边界采样）。
func (g *I2C) BoundaryByte(v int, ack bool, bndBitIdx int, offsetNs int64) {
	for i := 7; i >= 0; i-- {
		b := (v >> i) & 1
		if 7-i == bndBitIdx {
			g.Wait(g.Half)
			g.setSDA(b) // SDA 在 SCL 上升沿前 offsetNs 才变化
			g.Wait(-offsetNs)
			g.setSCL(1)
			g.Wait(g.Half + offsetNs)
			g.setSCL(0)
			g.Wait(g.Half)
			continue
		}
		g.bit(b)
	}
	g.ackBit(ack)
}

func (g *I2C) ackBit(ack bool) {
	level := 1
	if ack {
		level = 0
	}
	g.setSDA(level)
	g.Wait(g.Half)
	g.setSCL(1)
	g.Wait(g.Half)
	g.setSCL(0)
	g.Wait(g.Half)
}

func (g *I2C) Stop() {
	g.setSDA(0)
	g.Wait(g.Half)
	g.setSCL(1)
	g.Wait(g.Half)
	g.setSDA(1)
	g.Wait(g.Half)
}

// SPI 生成器（mode 0..3），支持多片选与边界采样注入。
type SPI struct {
	Half            int64
	t               int64
	Mode            int
	clk, mosi, miso int
	csLvl           map[string]int
	CLK, MOSI, MISO []model.Edge
	CS              map[string][]model.Edge
}

func NewSPI(half, startT int64, mode int) *SPI {
	cpol := 0
	if mode == 2 || mode == 3 {
		cpol = 1
	}
	return &SPI{
		Half: half, t: startT, Mode: mode,
		clk: cpol, mosi: 0, miso: 0,
		csLvl: map[string]int{},
		CS:    map[string][]model.Edge{},
	}
}

func (g *SPI) setCLK(v int) {
	if v != g.clk {
		g.clk = v
		g.CLK = append(g.CLK, model.Edge{T: g.t, Level: v})
	}
}

func (g *SPI) setMOSI(v int) {
	if v != g.mosi {
		g.mosi = v
		g.MOSI = append(g.MOSI, model.Edge{T: g.t, Level: v})
	}
}

func (g *SPI) setMISO(v int) {
	if v != g.miso {
		g.miso = v
		g.MISO = append(g.MISO, model.Edge{T: g.t, Level: v})
	}
}

func (g *SPI) setCS(name string, v int) {
	if cur, ok := g.csLvl[name]; !ok || cur != v {
		g.csLvl[name] = v
		g.CS[name] = append(g.CS[name], model.Edge{T: g.t, Level: v})
	}
}

func (g *SPI) Wait(ns int64) { g.t += ns }
func (g *SPI) T() int64      { return g.t }

func (g *SPI) CSActive(name string) { g.setCS(name, 0); g.Wait(g.Half) }
func (g *SPI) CSIdle(name string)   { g.Wait(g.Half); g.setCS(name, 1) }

// Byte 按 mode 0 时序发送一个字节（采样上升沿）。
func (g *SPI) Byte(mosiV, misoV int) {
	for i := 7; i >= 0; i-- {
		g.setMOSI((mosiV >> i) & 1)
		g.setMISO((misoV >> i) & 1)
		g.Wait(g.Half)
		g.setCLK(1 - g.clkIdle())
		g.Wait(g.Half)
		g.setCLK(g.clkIdle())
	}
}

// ByteBoundary 在 bndBitIdx 位上让 MOSI 跳变恰好落在采样沿时刻（边界采样）。
func (g *SPI) ByteBoundary(mosiV, misoV int, bndBitIdx int) {
	for i := 7; i >= 0; i-- {
		mb := (mosiV >> i) & 1
		g.setMISO((misoV >> i) & 1)
		if 7-i == bndBitIdx {
			g.Wait(g.Half)
			g.setMOSI(mb) // 与采样沿同一时刻
			g.setCLK(1 - g.clkIdle())
			g.Wait(g.Half)
			g.setCLK(g.clkIdle())
			continue
		}
		g.setMOSI(mb)
		g.Wait(g.Half)
		g.setCLK(1 - g.clkIdle())
		g.Wait(g.Half)
		g.setCLK(g.clkIdle())
	}
}

func (g *SPI) clkIdle() int {
	if g.Mode == 2 || g.Mode == 3 {
		return 1
	}
	return 0
}

// Sync 生成器：自定义同步协议（时钟 + 数据，字长可配）。
type Sync struct {
	Half      int64
	t         int64
	clk, data int
	CLK, DATA []model.Edge
}

func NewSync(half, startT int64) *Sync {
	return &Sync{Half: half, t: startT}
}

func (g *Sync) setCLK(v int) {
	if v != g.clk {
		g.clk = v
		g.CLK = append(g.CLK, model.Edge{T: g.t, Level: v})
	}
}

func (g *Sync) setDATA(v int) {
	if v != g.data {
		g.data = v
		g.DATA = append(g.DATA, model.Edge{T: g.t, Level: v})
	}
}

func (g *Sync) Wait(ns int64) { g.t += ns }
func (g *Sync) T() int64      { return g.t }

// Word 发送一个 bits 位的字：数据在时钟低电平期间建立，上升沿采样。
func (g *Sync) Word(v, bits int) {
	for i := bits - 1; i >= 0; i-- {
		g.setDATA((v >> i) & 1)
		g.Wait(g.Half)
		g.setCLK(1)
		g.Wait(g.Half)
		g.setCLK(0)
	}
}

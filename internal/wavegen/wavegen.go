// Package wavegen builds deterministic digital edge streams used by the
// offline case pack and by decoder tests.
package wavegen

import (
	"sort"

	"edgebench/internal/model"
)

// B is a waveform builder. Levels are raw probe levels (before any
// configured inversion).
type B struct {
	T      int64
	Levels map[string]int
	Edges  []model.Edge
	Gaps   []model.Gap
}

// New creates a builder and emits initial level edges at t=0.
func New(idle map[string]int) *B {
	b := &B{Levels: map[string]int{}}
	chs := make([]string, 0, len(idle))
	for ch := range idle {
		chs = append(chs, ch)
	}
	sort.Strings(chs)
	for _, ch := range chs {
		b.Levels[ch] = idle[ch]
		b.Edges = append(b.Edges, model.Edge{Channel: ch, Sample: 0, Level: idle[ch]})
	}
	return b
}

// Set drives a channel to a level at the current time (no-op if unchanged).
func (b *B) Set(ch string, lvl int) {
	if cur, ok := b.Levels[ch]; ok && cur == lvl {
		return
	}
	b.Levels[ch] = lvl
	b.Edges = append(b.Edges, model.Edge{Channel: ch, Sample: b.T, Level: lvl})
}

// Wait advances time without toggling anything.
func (b *B) Wait(dt int64) { b.T += dt }

// Gap inserts a capture discontinuity at the current time.
func (b *B) Gap(reason string) {
	b.Gaps = append(b.Gaps, model.Gap{Sample: b.T, Reason: reason})
}

// ---- I2C helpers (call with SCL low, SDA released) ----

// I2CStart emits a START condition from the idle state (both high).
func (b *B) I2CStart(sda, scl string, h int64) {
	b.Set(sda, 0)
	b.Wait(h)
	b.Set(scl, 0)
	b.Wait(h)
}

// I2CRestart emits a repeated START from within a transaction (SCL low).
func (b *B) I2CRestart(sda, scl string, h int64) {
	b.Set(sda, 1)
	b.Wait(h)
	b.Set(scl, 1)
	b.Wait(h)
	b.Set(sda, 0)
	b.Wait(h)
	b.Set(scl, 0)
	b.Wait(h)
}

// I2CBit clocks out one bit. sdaEdgeDelay, when >0, delays the SDA edge to
// that many samples after the SCL rising edge (boundary-sampling fault).
func (b *B) I2CBit(sda, scl string, bit int, h int64, sdaEdgeDelay int64) {
	if sdaEdgeDelay <= 0 {
		b.Set(sda, bit)
		b.Wait(h)
		b.Set(scl, 1)
		b.Wait(h)
		b.Set(scl, 0)
		return
	}
	// Keep old SDA through the clock edge, toggle just after it.
	b.Wait(h)
	b.Set(scl, 1)
	b.Wait(sdaEdgeDelay)
	b.Set(sda, bit)
	b.Wait(h - sdaEdgeDelay)
	b.Set(scl, 0)
}

// I2CByte clocks out 8 data bits plus the ACK/NACK bit (ack=true -> ACK).
func (b *B) I2CByte(sda, scl string, val int, ack bool, h int64) {
	for i := 7; i >= 0; i-- {
		b.I2CBit(sda, scl, (val>>i)&1, h, 0)
	}
	ackBit := 1
	if ack {
		ackBit = 0
	}
	b.I2CBit(sda, scl, ackBit, h, 0)
}

// I2CStretch holds SCL low for extra samples (slave clock stretching).
func (b *B) I2CStretch(extra int64) { b.Wait(extra) }

// I2CMissingClock toggles SDA twice during one SCL-low phase, simulating a
// missed clock edge: the bit alignment becomes ambiguous.
func (b *B) I2CMissingClock(sda string, intermediate, final int, h int64) {
	b.Set(sda, intermediate)
	b.Wait(h)
	b.Set(sda, final)
	b.Wait(h)
}

// I2CStop emits a STOP condition (SCL low -> high, then SDA released).
func (b *B) I2CStop(sda, scl string, h int64) {
	b.Set(sda, 0)
	b.Wait(h)
	b.Set(scl, 1)
	b.Wait(h)
	b.Set(sda, 1)
	b.Wait(h)
}

// ---- SPI helpers ----

// SPIByte clocks one byte, mode-0 style (data changes while clock low,
// sampled on rising edges). misoInvert writes raw inverted MISO levels to
// model a probe wired with hardware inversion.
func (b *B) SPIByte(sclk, mosi, miso string, out, in int, h int64, misoInvert bool) {
	for i := 7; i >= 0; i-- {
		mb := (out >> i) & 1
		pb := (in >> i) & 1
		if misoInvert {
			pb ^= 1
		}
		b.Set(mosi, mb)
		b.Set(miso, pb)
		b.Wait(h)
		b.Set(sclk, 1)
		b.Wait(h)
		b.Set(sclk, 0)
	}
}

// SPIByteCPHA1 clocks one byte with data changing just after the rising
// edge (correct sampling is on falling edges).
func (b *B) SPIByteCPHA1(sclk, mosi string, out int, h int64) {
	for i := 7; i >= 0; i-- {
		b.Set(sclk, 1)
		b.Wait(1)
		b.Set(mosi, (out>>i)&1)
		b.Wait(h - 1)
		b.Set(sclk, 0)
		b.Wait(h)
	}
}

// ---- custom synchronous helpers ----

// SyncByte clocks one byte on data/clk, sampled on rising clock edges.
func (b *B) SyncByte(data, clk string, val int, h int64) {
	for i := 7; i >= 0; i-- {
		b.Set(data, (val>>i)&1)
		b.Wait(h)
		b.Set(clk, 1)
		b.Wait(h)
		b.Set(clk, 0)
	}
}

// SyncMissingClock toggles data twice within one clock-low phase.
func (b *B) SyncMissingClock(data string, intermediate, final int, h int64) {
	b.Set(data, intermediate)
	b.Wait(h)
	b.Set(data, final)
	b.Wait(h)
}

// Package signals builds deterministic edge captures for the offline case
// pack. Times are expressed as integer nanoseconds so imported raw text keeps
// exact physical units ("120 ns").
package signals

import (
	"fmt"

	"edgebench/internal/model"
)

// Builder accumulates level transitions per alias.
type Builder struct {
	levels map[string]int
	edges  []model.WireEdge
	gaps   []model.WireGap
}

func New() *Builder {
	return &Builder{levels: map[string]int{}}
}

// Set forces a line to a level at ns, emitting an edge only on change.
func (b *Builder) Set(ns int64, alias string, level int) *Builder {
	cur, seen := b.levels[alias]
	if !seen {
		b.levels[alias] = level
		b.edges = append(b.edges, model.WireEdge{Channel: alias, At: fmt.Sprintf("%d ns", ns), Level: &level})
		return b
	}
	if cur != level {
		b.levels[alias] = level
		b.edges = append(b.edges, model.WireEdge{Channel: alias, At: fmt.Sprintf("%d ns", ns), Level: &level})
	}
	return b
}

// Gap records missing samples between nsFrom and nsTo.
func (b *Builder) Gap(nsFrom, nsTo int64) *Builder {
	b.gaps = append(b.gaps, model.WireGap{From: fmt.Sprintf("%d ns", nsFrom), To: fmt.Sprintf("%d ns", nsTo)})
	return b
}

func (b *Builder) Edges() []model.WireEdge { return b.edges }
func (b *Builder) Gaps() []model.WireGap   { return b.gaps }

// I2CTransaction emits one standard 7-bit write transaction onto SCL/SDA.
func (b *Builder) I2CTransaction(startNs int64, scl, sda string, addr7 int, data []int, nackLast bool) (start int64, stop int64) {
	t := startNs
	b.Set(t, scl, 1).Set(t, sda, 1)
	t += 100
	b.Set(t, sda, 0) // START: SDA falls while SCL high
	start = t
	t += 100
	b.Set(t, scl, 0)
	emitByte := func(value int, nack bool) {
		for i := 0; i < 8; i++ {
			bit := (value >> (7 - i)) & 1
			t += 100
			b.Set(t, sda, bit)
			t += 100
			b.Set(t, scl, 1)
			t += 100
			b.Set(t, scl, 0)
		}
		t += 100
		if nack {
			b.Set(t, sda, 1)
		} else {
			b.Set(t, sda, 0)
		}
		t += 100
		b.Set(t, scl, 1)
		t += 100
		b.Set(t, scl, 0)
	}
	emitByte(addr7<<1, false)
	for i, d := range data {
		emitByte(d, nackLast && i == len(data)-1)
	}
	t += 100
	b.Set(t, scl, 1)
	t += 100
	b.Set(t, sda, 1) // STOP while SCL high
	stop = t
	return start, stop
}

// I2CRepeatedStart emits addr write + repeated START + addr read + STOP.
func (b *Builder) I2CRepeatedStart(startNs int64, scl, sda string, addrW, addrR int) (start, stop int64) {
	t := startNs
	b.Set(t, scl, 1).Set(t, sda, 1)
	t += 100
	b.Set(t, sda, 0)
	start = t
	t += 100
	b.Set(t, scl, 0)
	emitByte := func(value int, ack bool) {
		for i := 0; i < 8; i++ {
			bit := (value >> (7 - i)) & 1
			t += 100
			b.Set(t, sda, bit)
			t += 100
			b.Set(t, scl, 1)
			t += 100
			b.Set(t, scl, 0)
		}
		t += 100
		if ack {
			b.Set(t, sda, 0)
		} else {
			b.Set(t, sda, 1)
		}
		t += 100
		b.Set(t, scl, 1)
		t += 100
		b.Set(t, scl, 0)
	}
	emitByte(addrW<<1, true)
	t += 100
	b.Set(t, sda, 1)
	t += 50
	b.Set(t, scl, 1)
	t += 50
	b.Set(t, sda, 0) // repeated START
	t += 100
	b.Set(t, scl, 0)
	emitByte(addrR<<1|1, true)
	t += 100
	b.Set(t, scl, 1)
	t += 100
	b.Set(t, sda, 1)
	stop = t
	return start, stop
}

// I2CStretch emits a frame with one very long SCL-low interval.
func (b *Builder) I2CStretch(startNs int64, scl, sda string, addr7 int, stretchNs int64) (start, stop int64) {
	t := startNs
	b.Set(t, scl, 1).Set(t, sda, 1)
	t += 100
	b.Set(t, sda, 0)
	start = t
	t += 100
	b.Set(t, scl, 0)
	for bi := 0; bi < 9; bi++ {
		if bi == 4 {
			t += stretchNs
		} else {
			t += 100
		}
		b.Set(t, sda, 0)
		t += 100
		b.Set(t, scl, 1)
		t += 100
		b.Set(t, scl, 0)
	}
	t += 100
	b.Set(t, scl, 1)
	t += 100
	b.Set(t, sda, 1)
	stop = t
	return start, stop
}

// SPIFrame emits one chip-select frame with bytes for one device.
func (b *Builder) SPIFrame(startNs int64, cs, clk, mosi string, activeHigh, cpol bool, words []int) (start, stop int64) {
	idle := 0
	if cpol {
		idle = 1
	}
	csActive := 0
	if activeHigh {
		csActive = 1
	}
	b.Set(startNs, clk, idle)
	b.Set(startNs, cs, csActive)
	start = startNs
	t := startNs + 100
	for _, w := range words {
		for i := 0; i < 8; i++ {
			bit := (w >> (7 - i)) & 1
			t += 100
			b.Set(t, mosi, bit)
			t += 100
			b.Set(t, clk, idle^1)
			t += 100
			b.Set(t, clk, idle)
		}
	}
	t += 100
	b.Set(t, cs, csActive^1)
	stop = t
	return start, stop
}

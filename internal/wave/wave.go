// Package wave builds deterministic digital edge streams for tests and for
// the offline case pack. All times are nanoseconds.
package wave

import "edgebench/internal/model"

const (
	I2CBit  = 1000 // full bit period
	I2CHalf = 500
)

// W accumulates edges; repeated writes of the same level are ignored so the
// output is always a clean transition list.
type W struct {
	Edges []model.Edge
	lvl   map[string]int
	set   map[string]bool
}

func New() *W { return &W{lvl: map[string]int{}, set: map[string]bool{}} }

// E records channel ch going to lvl at time t (no-op if already there).
func (w *W) E(ch string, t int64, lvl int) *W {
	if w.set[ch] && w.lvl[ch] == lvl {
		return w
	}
	w.Edges = append(w.Edges, model.Edge{Ch: ch, T: t, Level: lvl})
	w.lvl[ch] = lvl
	w.set[ch] = true
	return w
}

// Raw appends an edge unconditionally (used to build corrupt fixtures).
func (w *W) Raw(ch string, t int64, lvl int) *W {
	w.Edges = append(w.Edges, model.Edge{Ch: ch, T: t, Level: lvl})
	return w
}

// ---- I2C helpers (channels "SDA","SCL"; idle high) ----

func (w *W) I2CIdle(t int64) *W { return w.E("SDA", t, 1).E("SCL", t, 1) }

// I2CStart issues START at t; returns time when SCL is low and data may change.
func (w *W) I2CStart(t int64) int64 {
	w.E("SDA", t, 0)
	w.E("SCL", t+I2CHalf, 0)
	return t + I2CBit
}

// I2CBit writes one data bit starting at t (SCL low); returns next bit time.
func (w *W) I2CBitAt(t int64, bit int) int64 {
	w.E("SDA", t, bit)
	w.E("SCL", t+I2CHalf, 1)
	w.E("SCL", t+I2CBit, 0)
	return t + I2CBit
}

// I2CByte writes 8 data bits plus a 9th ACK(0)/NACK(1) slot.
func (w *W) I2CByte(t int64, val int, ack bool) int64 {
	for k := 7; k >= 0; k-- {
		t = w.I2CBitAt(t, (val>>k)&1)
	}
	ackBit := 1
	if ack {
		ackBit = 0
	}
	return w.I2CBitAt(t, ackBit)
}

// I2CStretchBit writes one bit but the slave holds SCL low for extraNs.
func (w *W) I2CStretchBit(t int64, bit int, extraNs int64) int64 {
	w.E("SDA", t, bit)
	w.E("SCL", t+I2CHalf+extraNs, 1)
	w.E("SCL", t+I2CBit+extraNs, 0)
	return t + I2CBit + extraNs
}

// I2CStop issues STOP at t (SCL low, SDA low expected); returns end time.
func (w *W) I2CStop(t int64) int64 {
	w.E("SDA", t, 0)
	w.E("SCL", t+I2CHalf, 1)
	w.E("SDA", t+I2CBit, 1)
	return t + I2CBit
}

// ---- SPI helpers (channels "SCLK","MOSI","CS1","CS2"; mode 0 shape) ----

// SPIByte writes one byte at bit period p; MOSI changes off ns after the
// preceding falling clock edge (off=0 makes the data edge simultaneous with
// the falling clock edge, i.e. a boundary sample for mode 1/2).
func (w *W) SPIByte(t int64, val int, p, off int64) int64 {
	half := p / 2
	for k := 7; k >= 0; k-- {
		w.E("MOSI", t+off, (val>>k)&1)
		w.E("SCLK", t+half, 1)
		w.E("SCLK", t+p, 0)
		t += p
	}
	return t
}

// SPIByteBoundary is SPIByte but the MOSI transition for the bit at
// boundaryBit (0=MSB) happens exactly on the falling clock edge.
func (w *W) SPIByteBoundary(t int64, val int, p, off int64, boundaryBit int) int64 {
	half := p / 2
	for k := 7; k >= 0; k-- {
		bitIdx := 7 - k
		o := off
		if bitIdx == boundaryBit {
			o = 0 // simultaneous with the falling edge that ends the previous bit
		}
		w.E("MOSI", t+o, (val>>k)&1)
		w.E("SCLK", t+half, 1)
		w.E("SCLK", t+p, 0)
		t += p
	}
	return t
}

// SPIFrame wraps bytes in a chip-select assertion on channel cs.
func (w *W) SPIFrame(cs string, t int64, p, off int64, vals ...int) int64 {
	w.E(cs, t, 0)
	t += p
	for _, v := range vals {
		t = w.SPIByte(t, v, p, off)
	}
	w.E(cs, t+p, 1)
	return t + p
}

// ---- sync helpers (channels "DCLK","DAT") ----

// SyncWord writes wordSize bits MSB-first, data stable before rising clock.
func (w *W) SyncWord(t int64, val int, wordSize int, p int64) int64 {
	half := p / 2
	for k := wordSize - 1; k >= 0; k-- {
		w.E("DAT", t, (val>>k)&1)
		w.E("DCLK", t+half, 1)
		w.E("DCLK", t+p, 0)
		t += p
	}
	return t
}

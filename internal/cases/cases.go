// Package cases builds the domain-specific offline case pack for the
// "edge decode bench": logical-analyzer edges decoded into I2C, SPI and
// custom synchronous transactions with overlap candidates.
package cases

import (
	"edgebench/internal/store"
)

// Builder edges are expressed in picoseconds against an explicit sample
// rate/time base. Times use a 100 MHz-equivalent grid (10 ns) for the SPI
// and custom buses and a 2.5 us SCL period for I2C.

// t units: helpers work in nanoseconds at import time; conversion keeps the
// original representation.
const ns = 1

func rq(v string, unit string) store.RawQuantity { return store.RawQuantity{Value: v, Unit: unit} }

// edgeBuilder assembles edges for named channels.
type edgeBuilder struct {
	edges []store.InputEdge
}

func (b *edgeBuilder) at(alias string, tNS int64, level int) {
	b.edges = append(b.edges, store.InputEdge{Alias: alias, Level: level, Time: rq(itoa(tNS), "ns")})
}

func itoa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [32]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}

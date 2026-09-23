// Package casepack builds the offline domain case "边沿译码台：逻辑分析波形、
// I2C/SPI 交易与重叠候选". It ships a baseline that preserves raw edge
// times, sample rate, channel aliases, level inversion, clock polarity and
// decoder version; then one parameter revision, one single-source local
// change, and two conflicting human decisions.
package casepack

import (
	"edgedeck/internal/decode"
	"edgedeck/internal/export"
	"edgedeck/internal/model"
)

// ---- edge emit helpers (times in ns, the coordinate basis of the case) ----

type emitter struct{ edges []model.Edge }

func (e *emitter) add(ch string, t int64, lvl int) {
	e.edges = append(e.edges, model.Edge{
		Channel: ch, TimeRaw: itoa64(t) + "ns", TimeNS: t, Level: lvl,
	})
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

// i2cStart emits START at t; returns first bit-cell setup time.
func i2cStart(e *emitter, t int64) int64 {
	e.add("SDA", t, 0)
	e.add("SCL", t+400, 0)
	return t + 800
}

// i2cBit emits one bit cell starting at t (SCL low, SDA set at t).
func i2cBit(e *emitter, t int64, bit int) int64 {
	e.add("SDA", t, bit)
	e.add("SCL", t+300, 1)
	e.add("SCL", t+700, 0)
	return t + 1000
}

// i2cBitStretch emits a bit cell where the slave stretches SCL low.
func i2cBitStretch(e *emitter, t int64, bit int, stretch int64) int64 {
	e.add("SDA", t, bit)
	e.add("SCL", t+300+stretch, 1)
	e.add("SCL", t+700+stretch, 0)
	return t + 1000 + stretch
}

// i2cBitBoundary emits a bit whose SDA transition coincides with the SCL
// rising edge (boundary sampling).
func i2cBitBoundary(e *emitter, t int64, bit int) int64 {
	e.add("SCL", t+300, 1)
	e.add("SDA", t+300, bit) // moves exactly at the sampling edge
	e.add("SCL", t+700, 0)
	return t + 1000
}

func i2cByte(e *emitter, t int64, v int, ack int) int64 {
	for i := 7; i >= 0; i-- {
		t = i2cBit(e, t, (v>>i)&1)
	}
	return i2cBit(e, t, ack)
}

// i2cRepeatedStart emits Sr from SCL-low state.
func i2cRepeatedStart(e *emitter, t int64) int64 {
	e.add("SDA", t, 1)
	e.add("SCL", t+300, 1)
	e.add("SDA", t+700, 0)
	e.add("SCL", t+1100, 0)
	return t + 1400
}

// i2cStop emits STOP from SCL-low state (SDA currently 0).
func i2cStop(e *emitter, t int64) int64 {
	e.add("SCL", t+300, 1)
	e.add("SDA", t+700, 1)
	return t + 1400
}

// ---- captures ----

func i2cCapture() *model.Capture {
	e := &emitter{}
	// Transaction 1: clean write 0x50 addr(W) ACK, 0xA5 ACK, STOP.
	t := i2cStart(e, 1000)
	t = i2cByte(e, t, 0xA0, 0) // addr 0x50 + W, ACK
	t = i2cByte(e, t, 0xA5, 0) // data, ACK
	t = i2cStop(e, t)

	// Transaction 2: repeated START + clock stretching + boundary bit +
	// missing clock edges (partial byte) + trailing data gap.
	t = i2cStart(e, t+2000)
	t = i2cByte(e, t, 0xA0, 0)       // addr 0x50 W ACK
	t = i2cRepeatedStart(e, t)       // repeated START, no STOP
	t = i2cByte(e, t, 0xA1, 0)       // addr 0x50 R ACK
	t = i2cBitStretch(e, t, 1, 6000) // stretched bit (SCL low 6300ns+)
	t = i2cBit(e, t, 1)              //
	t = i2cBitBoundary(e, t, 1)      // boundary-sampled bit
	t = i2cBit(e, t, 0)              //
	t = i2cBit(e, t, 1)              // -> only 5 bits, then STOP:
	t = i2cStop(e, t)                // missing clock edges -> partial frame
	// data gap region: a lone START + 3 bits, then >GapNS silence, then
	// more bits that must NOT be stitched into the same transaction.
	t = i2cStart(e, t+2000)
	t = i2cBit(e, t, 0)
	t = i2cBit(e, t, 1)
	t = i2cBit(e, t, 1)
	// gap of 50000ns (> GapNS=20000) then trailing bits: decoder closes tx.
	t = i2cBit(e, t+50000, 0)
	t = i2cBit(e, t, 1)
	_ = t

	return &model.Capture{
		ID: model.HashID("cap", "i2c-baseline", e.edges), Name: "i2c-baseline",
		SampleRateHz: 10_000_000, ClockPolarity: "normal",
		Channels: []model.Channel{
			{Alias: "SDA", Probe: "P0/ch1", Role: "sda"},
			{Alias: "SCL", Probe: "P0/ch2", Role: "scl"},
		},
		Edges: e.edges,
	}
}

func spiEdges(polarity string) *model.Capture {
	e := &emitter{}
	// CS1 frame: 12 SCLK cycles (1 byte + partial nibble), mode unknown.
	e.add("CS1", 2000, 0)
	t := int64(2400)
	bits := []int{1, 0, 1, 1, 0, 0, 1, 0, 1, 1, 0, 1} // 0xB2 + nibble 0b1011
	for i, b := range bits {
		if i == 3 {
			// boundary sampling: MOSI moves with the clock edge
			e.add("SCLK", t, 1)
			e.add("MOSI", t, b)
			e.add("SCLK", t+400, 0)
		} else {
			e.add("MOSI", t, b)
			e.add("SCLK", t+200, 1)
			e.add("SCLK", t+600, 0)
		}
		t += 800
	}
	e.add("CS1", t+200, 1)
	// CS2 frame overlapping CS1's tail is impossible here (CS1 closed);
	// instead CS2 overlaps a second CS1 frame to exercise multi-CS.
	cs1b := t + 2000
	e.add("CS1", cs1b, 0)
	e.add("CS2", cs1b+1600, 0) // overlaps CS1 while both active
	t2 := cs1b + 400
	for i := 0; i < 8; i++ { // CS1: 0x3C
		b := (0x3C >> (7 - i)) & 1
		e.add("MOSI", t2, b)
		e.add("SCLK", t2+200, 1)
		e.add("SCLK", t2+600, 0)
		t2 += 800
	}
	e.add("CS1", t2+200, 1)
	t3 := cs1b + 2000 // CS2 frame: 0x7E on its own clock stretch
	for i := 0; i < 8; i++ {
		b := (0x7E >> (7 - i)) & 1
		e.add("MOSI", t3, b)
		e.add("SCLK", t3+200, 1)
		e.add("SCLK", t3+600, 0)
		t3 += 800
	}
	e.add("CS2", t3+200, 1)
	// data gap inside a CS1 frame: 4 clocks, 40000ns silence, 4 clocks.
	g := t3 + 3000
	e.add("CS1", g, 0)
	tg := g + 400
	for i := 0; i < 4; i++ {
		e.add("MOSI", tg, 1)
		e.add("SCLK", tg+200, 1)
		e.add("SCLK", tg+600, 0)
		tg += 800
	}
	tg += 40000 // gap — frame must close, not stitch
	for i := 0; i < 4; i++ {
		e.add("MOSI", tg, 0)
		e.add("SCLK", tg+200, 1)
		e.add("SCLK", tg+600, 0)
		tg += 800
	}
	e.add("CS1", tg+200, 1)

	return &model.Capture{
		ID: model.HashID("cap", "spi-multi", polarity, e.edges), Name: "spi-multi",
		SampleRateHz: 10_000_000, ClockPolarity: polarity,
		Channels: []model.Channel{
			{Alias: "SCLK", Probe: "P1/ch1", Role: "sclk"},
			{Alias: "MOSI", Probe: "P1/ch2", Role: "mosi"},
			{Alias: "MISO", Probe: "P1/ch3", Role: "miso"},
			{Alias: "CS1", Probe: "P1/ch4", Role: "cs", CSIndex: 0},
			{Alias: "CS2", Probe: "P1/ch5", Role: "cs", CSIndex: 1},
		},
		Edges: e.edges,
	}
}

func syncCapture() *model.Capture {
	e := &emitter{}
	// frame 1: two words 0x55, 0x0F ; frame 2 after gap: 0x99 (one bit
	// boundary-sampled).
	t := int64(3000)
	put := func(v int, boundaryBit int) {
		for i := 7; i >= 0; i-- {
			b := (v >> i) & 1
			if i == boundaryBit {
				e.add("CLK", t, 1)
				e.add("DATA", t, b)
			} else {
				e.add("DATA", t, b)
				e.add("CLK", t+200, 1)
			}
			e.add("CLK", t+400, 0)
			t += 800
		}
	}
	put(0x55, -1)
	put(0x0F, -1)
	t += 40000 // gap closes frame
	put(0x99, 2)
	return &model.Capture{
		ID: model.HashID("cap", "sync-stream", e.edges), Name: "sync-stream",
		SampleRateHz: 10_000_000, ClockPolarity: "normal",
		Channels: []model.Channel{
			{Alias: "CLK", Probe: "P2/ch1", Role: "clk"},
			{Alias: "DATA", Probe: "P2/ch2", Role: "data", Inverted: true}, // probe inversion preserved
		},
		Edges: e.edges,
	}
}

// ---- params ----

func paramsV1() *model.Params {
	return &model.Params{
		ID: model.HashID("par", "v1"), Version: 1, DecoderVersion: decode.DecoderVersion,
		Protocol: "i2c", BoundaryNS: 50, GapNS: 20000, StretchNS: 2000,
		MaxCandidates: 32, MaxEdges: 100000,
	}
}

// paramsV2 is the parameter revision: wider boundary window reclassifies
// the coincident SDA/SCL edge as boundary-sampled.
func paramsV2(parent *model.Params) *model.Params {
	p := *parent
	p.ID = model.HashID("par", "v2")
	p.Version = 2
	p.ParentID = parent.ID
	p.BoundaryNS = 120
	return &p
}

func spiParams() *model.Params {
	return &model.Params{
		ID: model.HashID("par", "spi-v1"), Version: 1, DecoderVersion: decode.DecoderVersion,
		Protocol: "spi", BoundaryNS: 50, GapNS: 20000, SPIMode: "unknown",
		MaxCandidates: 32, MaxEdges: 100000,
	}
}

func syncParams() *model.Params {
	return &model.Params{
		ID: model.HashID("par", "sync-v1"), Version: 1, DecoderVersion: decode.DecoderVersion,
		Protocol: "sync", BoundaryNS: 50, GapNS: 20000, SyncBits: 8,
		MaxCandidates: 32, MaxEdges: 100000,
	}
}

// BuildArchive assembles the full offline case: baseline materials, one
// parameter revision, one single-source local change (clock polarity flip
// on the SPI capture only), and two conflicting human decisions.
func BuildArchive() *export.Archive {
	a := &export.Archive{Version: export.ArchiveVersion}

	i2cCap := i2cCapture()
	spiCap := spiEdges("normal")
	syncCap := syncCapture()
	// source change: only the direct source "clock polarity" is flipped;
	// edges, aliases, inversion, sample rate are untouched.
	spiFlip := spiEdges("inverted")
	spiFlip.ID = model.HashID("cap", "spi-multi-polflip", spiFlip.Edges)
	spiFlip.Name = "spi-multi-polflip"

	a.Captures = []model.Capture{*i2cCap, *spiCap, *syncCap, *spiFlip}

	p1 := paramsV1()
	p2 := paramsV2(p1)
	sp := spiParams()
	yp := syncParams()
	a.Params = []model.Params{*p1, *p2, *sp, *yp}

	run := func(c *model.Capture, p *model.Params) model.Decode {
		d := decode.Run(c, p)
		return *d
	}
	dI2Cv1 := run(i2cCap, p1)
	dI2Cv2 := run(i2cCap, p2) // param revision re-decode
	dSPI := run(spiCap, sp)
	dSPIFlip := run(spiFlip, sp) // local source change re-decode
	dSync := run(syncCap, yp)
	a.Decodes = []model.Decode{dI2Cv1, dI2Cv2, dSPI, dSPIFlip, dSync}

	// conflicting human decisions on the same SPI transaction candidate.
	if len(dSPI.Transactions) > 0 && len(dSPI.Transactions[0].Candidates) > 0 {
		tx := dSPI.Transactions[0]
		cand := tx.Candidates[0]
		a.Decisions = append(a.Decisions,
			model.Decision{
				ID: model.HashID("decn", "accept", cand.ID), DecodeID: dSPI.ID,
				Transaction: tx.ID, CandidateID: cand.ID, Action: "accept",
				Note: "reviewer A: mode-0 reading matches schematic",
			},
			model.Decision{
				ID: model.HashID("decn", "reject", cand.ID), DecodeID: dSPI.ID,
				Transaction: tx.ID, CandidateID: cand.ID, Action: "reject",
				Note: "reviewer B: SPI mode unproven, candidate not unique",
			},
		)
	}
	return a
}

// Package casepack is the offline domain case pack for
// "edge decoding bench: logic-analyzer waveforms, I2C/SPI transactions and
// overlapping candidates". It is embedded into the binary; no generic
// spreadsheet material is used.
package casepack

import (
	"encoding/json"
	"fmt"

	"edgebench/internal/ingest"
	"edgebench/internal/model"
)

// File is one material file in the pack.
type File struct {
	Name    string
	Kind    string // capture|script|readme
	Content []byte
}

const gridNS = 10 // time grid

type rb struct {
	edges  []model.Edge
	perCh  map[string]int
	cursor map[string]float64
	levels map[string]bool
}

func newRB() *rb {
	return &rb{perCh: map[string]int{}, cursor: map[string]float64{}, levels: map[string]bool{}}
}

func (b *rb) init(ch string, idle bool, t float64) {
	b.levels[ch] = idle
	b.cursor[ch] = t
}

// pulse toggles the channel twice starting at t (ns), pulseLen ns each phase.
func (b *rb) pulse(ch string, t, pulseLen float64) {
	b.set(ch, t, !b.levels[ch])
	b.set(ch, t+pulseLen, !b.levels[ch])
}

func (b *rb) set(ch string, tNS float64, level bool) {
	if b.levels[ch] == level {
		return
	}
	b.edges = append(b.edges, model.Edge{
		ID:    fmt.Sprintf("E-MAIN-%d", len(b.edges)),
		T:     model.Qty{Raw: fmt.Sprintf("%gns", tNS), Unit: "ns", V: tNS * 1e-9, Ok: true},
		Level: bitS(level), Chan: ch,
	})
	b.levels[ch] = level
	b.perCh[ch]++
}

func bitS(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// i2cFrame emits address/data byte bits on sda with scl clocks, in ns.
// Returns the time after the ACK clock.
func i2cByte(b *rb, scl, sda string, startNS float64, val int, ack bool, jitterNs float64) float64 {
	t := startNS
	bitW := 100.0
	lo := 60.0
	for i := 7; i >= 0; i-- {
		want := val&(1<<uint(i)) != 0
		b.set(sda, t+10, want)
		b.pulse(scl, t+30, lo) // low 30..90, high 90..130 roughly
		t += bitW
	}
	b.set(sda, t+10, !ack) // ACK=0 (pulled low), NACK=1
	if jitterNs != 0 {
		b.set(sda, t+30+jitterNs, !ack) // boundary-adjacent edge
	}
	b.pulse(scl, t+30, lo)
	t += bitW
	return t
}

func probe(ch, alias, inv, role, clkPol string) model.Probe {
	return model.Probe{Channel: ch, Alias: alias, InvertedRaw: inv, Inverted: inv == "true",
		Role: role, ClkPolRaw: clkPol,
		Volt: model.Qty{Raw: "3.3V", Unit: "V", V: 3.3, Ok: true}}
}

// BuildMainCapture creates the review-case capture containing all three buses
// and every boundary situation exercised by the replay script.
func BuildMainCapture() model.Capture {
	b := newRB()
	const sclC, sdaC = "CH1", "CH2"
	const sckC, mosiC, cs1C, cs2C = "CH3", "CH4", "CH5", "CH6"
	const syncC, dC = "CH7", "CH8"
	for _, ch := range []string{sclC, sdaC, sckC, mosiC, cs1C, cs2C, syncC, dC} {
		b.init(ch, true, 0)
	}
	c := model.Capture{
		FormatVersion: ingest.FormatVersion,
		SourceFile:    "bench/main-edge-capture.jsonl",
		CapturedAt:    "2026-09-23T09:00:00+09:00",
		SampleRate:    model.Qty{Raw: "100MHz", Unit: "MHz", V: 1e8, Ok: true},
		Probes: []model.Probe{
			probe(sclC, "I2C_SCL", "false", "scl", "0"),
			probe(sdaC, "I2C_SDA", "false", "sda", ""),
			probe(sckC, "SPI_SCK", "false", "sck", "0"),
			probe(mosiC, "SPI_MOSI", "false", "mosi", ""),
			probe(cs1C, "SPI_CS_TEMP", "false", "cs", ""),
			probe(cs2C, "SPI_CS_EEPROM", "false", "cs", ""),
			probe(syncC, "SYNC_CLK", "false", "sync", ""),
			probe(dC, "SYNC_DAT", "true", "data", ""), // level inversion preserved
		},
	}

	// ---- I2C: write A3 + data 11, repeated START, read A3 + data, NACK ----
	t := 200.0
	b.set(sdaC, t, false) // START
	b.set(sclC, t+40, false)
	t += 100
	t = i2cByte(b, sclC, sdaC, t, 0xA3, true, 0)
	t = i2cByte(b, sclC, sdaC, t, 0x11, true, 0)
	// repeated START: SDA falls while SCL high
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, false) // SCL high then SDA fall = START
	b.set(sclC, t+90, false)
	t += 200
	t = i2cByte(b, sclC, sdaC, t, 0xA3|1, true, 0)
	t = i2cByte(b, sclC, sdaC, t, 0x7F, false, 0) // NACK
	// STOP
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, true)
	t += 200

	// ---- I2C clock stretching in next transaction ----
	b.set(sdaC, t, false)
	b.set(sclC, t+40, false)
	t += 100
	t = i2cByte(b, sclC, sdaC, t, 0x50, true, 0)
	// stretch: SCL held low 500 ns (> default 200 ns threshold)
	stretchStart := t
	b.set(sdaC, t+10, false)
	b.pulse(sclC, t+530, 60) // delayed clock for first data bit region
	t = stretchStart + 100 + 500
	t = i2cByte(b, sclC, sdaC, t, 0x22, true, 0)
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, true) // STOP
	t += 300

	// ---- I2C data gap (>5000 ns default threshold): must not stitch ----
	b.set(sdaC, t, false)
	b.set(sclC, t+40, false)
	t += 100
	t = i2cByte(b, sclC, sdaC, t, 0x90, true, 0)
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, true) // STOP
	t += 6000
	b.set(sdaC, t, false)
	b.set(sclC, t+40, false)
	t += 100
	t = i2cByte(b, sclC, sdaC, t, 0x91, true, 0)
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, true) // STOP
	t += 300

	// ---- I2C boundary sampling + missing clock edge (partial frame) ----
	b.set(sdaC, t, false)
	b.set(sclC, t+40, false)
	t += 100
	t = i2cByte(b, sclC, sdaC, t, 0xC4, true, 6) // SDA edge 6 ns from SCL edge
	t = i2cBytePartial(b, sclC, sdaC, t, 0x3, 5) // only 5 trailing clock edges
	b.set(sclC, t+30, true)
	b.set(sdaC, t+50, true) // STOP
	t += 400

	// ---- SPI device 1 (CS_TEMP): 12 bits -> missing edge, mode ambiguous ----
	spiFrame(b, sckC, mosiC, cs1C, 8000.0, 0b101100101101, 12)
	// ---- SPI device 2 (CS_EEPROM): full 16 bits, 0xA5 0x3C ----
	spiFrame(b, sckC, mosiC, cs2C, 10500.0, 0xA53C, 16)
	// ---- SPI device 1 second frame: 8 bits 0x6E ----
	spiFrame(b, sckC, mosiC, cs1C, 13000.0, 0x6E, 8)

	// ---- custom synchronous: 16 sync pulses, data 0x9C 0x41 ----
	customBurst(b, syncC, dC, 15500.0, []int{0x9C, 0x41})

	c.Edges = b.edges
	return c
}

// i2cBytePartial emits only nBits SCL clocks with given low bits.
func i2cBytePartial(b *rb, scl, sda string, startNS float64, lowBits, nBits int) float64 {
	t := startNS
	bitW := 100.0
	for i := 0; i < nBits; i++ {
		want := lowBits&(1<<uint(nBits-1-i)) != 0
		b.set(sda, t+10, want)
		b.pulse(scl, t+30, 60)
		t += bitW
	}
	return t
}

// spiFrame emits nBits of data MSB-first with CS framing.
func spiFrame(b *rb, sck, mosi, cs string, startNS float64, val uint32, nBits int) {
	t := startNS
	b.set(cs, t, false) // assert
	t += 30
	for i := nBits - 1; i >= 0; i-- {
		want := val&(1<<uint(i)) != 0
		b.set(mosi, t, want)
		b.set(sck, t+20, true)
		b.set(sck, t+60, false)
		t += 100
	}
	b.set(cs, t+10, true) // deassert
}

func customBurst(b *rb, syn, data string, startNS float64, vals []int) {
	t := startNS
	idx := 0
	for _, v := range vals {
		for i := 7; i >= 0; i-- {
			want := v&(1<<uint(i)) != 0
			b.set(data, t, want)
			b.set(syn, t+10, true)
			b.set(syn, t+40, false)
			t += 80
			idx++
		}
	}
}

// MustMarshal is a test/seed helper.
func MustMarshal(v any) []byte {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return out
}

// ---- counter baselines: each is a small dedicated capture so a mutation of
// one direct source has a verifiable blast radius. ----

func mkI2CBase(name string, opts i2cOpt) model.Capture {
	b := newRB()
	scl, sda := "SCL", "SDA"
	b.init(scl, true, 0)
	b.init(sda, true, 0)
	stretch := 0.0
	if opts.stretch {
		stretch = 400
	}
	c := model.Capture{
		FormatVersion: ingest.FormatVersion, SourceFile: "counter/" + name + ".json",
		SampleRate: model.Qty{Raw: "100MHz", Unit: "MHz", V: 1e8, Ok: true},
		Probes: []model.Probe{
			probe("SCL", "SCL", "false", "scl", opts.pol),
			probe("SDA", "SDA", "false", "sda", ""),
		},
	}
	t := 100.0
	b.set(sda, t, false)
	b.set(scl, t+40, false)
	t += 100
	if stretch > 0 {
		t = i2cByte(b, scl, sda, t, 0x40, true, 0)
		b.set(sda, t+10, false)
		b.pulse(scl, t+30+stretch, 60)
		t += 100 + stretch
	} else {
		t = i2cByte(b, scl, sda, t, 0x40, true, 0)
	}
	t = i2cByte(b, scl, sda, t, opts.byte2, true, 0)
	b.set(scl, t+30, true)
	b.set(sda, t+50, true)
	if opts.rstart {
		// repeated START + read address before STOP
		b.set(scl, t+90, false)
		b.set(sda, t+110, false)
		t += 200
		t = i2cByte(b, scl, sda, t, 0x41, true, 0)
		b.set(scl, t+30, true)
		b.set(sda, t+50, true)
	}
	c.Edges = b.edges
	return c
}

type i2cOpt struct {
	pol     string
	byte2   int
	stretch bool
	rstart  bool
}

func mkSPIBase(name string, edges int, multiCS bool) model.Capture {
	b := newRB()
	sck, mosi, csA, csB := "SCK", "MOSI", "CS_A", "CS_B"
	for _, ch := range []string{sck, mosi, csA, csB} {
		b.init(ch, true, 0)
	}
	c := model.Capture{
		FormatVersion: ingest.FormatVersion, SourceFile: "counter/" + name + ".json",
		SampleRate: model.Qty{Raw: "100MHz", Unit: "MHz", V: 1e8, Ok: true},
		Probes: []model.Probe{
			probe("SCK", "SCK", "false", "sck", ""),
			probe("MOSI", "MOSI", "false", "mosi", ""),
			probe("CS_A", "CS_A", "false", "cs", ""),
			probe("CS_B", "CS_B", "false", "cs", ""),
		},
	}
	var v uint32 = 0b110010100011
	spiFrame(b, sck, mosi, csA, 500.0, v, edges)
	if multiCS {
		spiFrame(b, sck, mosi, csB, 3000.0, 0x77, 8)
	}
	c.Edges = b.edges
	return c
}

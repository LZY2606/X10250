// Package wavgen generates the deterministic waveforms of the offline domain case
// "边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选".
// It generates deterministic edge captures (raw representations preserved)
// and replays the full narrative: baseline import, one parameter revision,
// one local source-data change and one pair of conflicting human decisions.
package wavgen

import (
	"fmt"
	"strconv"

	"edgedesk/internal/model"
)

// ---------------------------------------------------------------- waveform gen

type gen struct {
	edges  []model.Edge
	levels map[string]int
	t      int64
	seq    int
}

func newGen(idle map[string]int) *gen {
	lv := map[string]int{}
	for k, v := range idle {
		lv[k] = v
	}
	return &gen{levels: lv}
}

func (g *gen) set(ch string, lv int) {
	if cur, ok := g.levels[ch]; ok && cur == lv {
		return
	}
	g.levels[ch] = lv
	g.edges = append(g.edges, model.Edge{
		Seq: g.seq, ChannelID: ch, TimeRaw: strconv.FormatInt(g.t, 10), Sample: g.t, Level: lv})
	g.seq++
}

func (g *gen) wait(n int64) { g.t += n }

// ------------------------------------------------------------- i2c generator

type i2cInfo struct {
	MissingRegionStart int64
	MissingRegionEnd   int64
	MissingEdges       []model.Edge // edges to add back in the source-change version
}

func i2cStart(g *gen, scl, sda string, half int64) {
	g.set(sda, 1)
	g.wait(2)
	g.set(scl, 1)
	g.wait(half)
	g.set(sda, 0)
	g.wait(half)
	g.set(scl, 0)
}

func i2cStop(g *gen, scl, sda string, half int64) {
	g.set(sda, 0)
	g.wait(half)
	g.set(scl, 1)
	g.wait(half)
	g.set(sda, 1)
	g.wait(half)
}

func i2cByte(g *gen, scl, sda string, half int64, b byte, ack bool, stretchExtra int64) {
	for i := 7; i >= 0; i-- {
		g.set(sda, int(b>>i)&1)
		g.wait(half)
		g.set(scl, 1)
		g.wait(half)
		g.set(scl, 0)
	}
	if ack {
		g.set(sda, 0)
	} else {
		g.set(sda, 1)
	}
	g.wait(half)
	if stretchExtra > 0 {
		g.wait(stretchExtra) // slave holds SCL low: clock stretching
	}
	g.set(scl, 1)
	g.wait(half)
	g.set(scl, 0)
}

// genI2CBus builds: write 2 bytes, repeated START, read 1 byte with clock
// stretching, NO STOP, long data gap, then START + one byte whose capture
// lost one SCL pulse (missing clock edges) + STOP.
func genI2CBus(scl, sda string, half int64) ([]model.Edge, i2cInfo) {
	g := newGen(map[string]int{scl: 1, sda: 1})
	g.wait(20)
	// T1: addr 0x50 write, data 0xAB 0x0C
	i2cStart(g, scl, sda, half)
	i2cByte(g, scl, sda, half, 0xA0, true, 0)
	i2cByte(g, scl, sda, half, 0xAB, true, 0)
	i2cByte(g, scl, sda, half, 0x0C, true, 0)
	// repeated START + read
	i2cStart(g, scl, sda, half)
	i2cByte(g, scl, sda, half, 0xA1, true, 0)
	i2cByte(g, scl, sda, half, 0x3C, false, 35) // NACK + clock stretching (35 > threshold 30, < gap 40)
	// no STOP: data gap
	g.wait(120)
	// T2: START, one byte with a lost SCL pulse, STOP
	i2cStart(g, scl, sda, half)
	// byte 0xFE but the capture dropped the clock pulse at bit 4:
	// SDA wiggles twice while SCL stays low (signature of missing clock)
	b := byte(0xFE)
	for i := 7; i >= 0; i-- {
		g.set(sda, int(b>>i)&1)
		g.wait(half)
		if i == 4 {
			g.set(sda, 0) // spurious data edges while SCL low
			g.wait(2)
			g.set(sda, 1)
			g.wait(half - 2)
			continue // no SCL pulse for this bit
		}
		g.set(scl, 1)
		g.wait(half)
		g.set(scl, 0)
	}
	g.set(sda, 0) // ACK
	g.wait(half)
	g.set(scl, 1)
	g.wait(half)
	g.set(scl, 0)
	i2cStop(g, scl, sda, half)
	g.wait(20)

	info := i2cInfo{}
	return g.edges, info
}

// ------------------------------------------------------------------ captures

func rate() model.SampleRate { return model.SampleRate{Raw: "1MHz", Hz: 1e6} }

// I2CCapture returns the i2c material. variant 0 = baseline (with missing
// clock edges), variant 1 = source-changed (missing SCL edges restored).
func I2CCapture(variant int) (*model.Capture, int64, int64) {
	scl, sda := "ch0", "ch1"
	edges, _ := genI2CBus(scl, sda, 8)
	// locate the missing-clock region: the SDA double-toggle while SCL low
	var regionStart, regionEnd int64
	if variant == 1 {
		// find region: longest SCL-low stretch without stretch flag is complex;
		// the generator places the anomaly deterministically: find the two SDA
		// edges 2 samples apart while SCL low.
		var sclLowAt int64 = -1
		for i, e := range edges {
			if e.ChannelID == scl && e.Level == 0 {
				sclLowAt = e.Sample
			}
			if e.ChannelID == scl && e.Level == 1 {
				sclLowAt = -1
			}
			if e.ChannelID == sda && sclLowAt >= 0 && i+1 < len(edges) &&
				edges[i+1].ChannelID == sda && edges[i+1].Sample-e.Sample == 2 {
				regionStart = e.Sample
				regionEnd = edges[i+1].Sample
			}
		}
		// source change: drop the spurious SDA wiggle and restore the
		// missing SCL pulse just before it
		var kept []model.Edge
		for _, e := range edges {
			if e.ChannelID == sda && (e.Sample == regionStart || e.Sample == regionEnd) {
				continue
			}
			kept = append(kept, e)
		}
		kept = append(kept,
			model.Edge{ChannelID: scl, TimeRaw: strconv.FormatInt(regionStart-3, 10), Sample: regionStart - 3, Level: 1},
			model.Edge{ChannelID: scl, TimeRaw: strconv.FormatInt(regionStart-1, 10), Sample: regionStart - 1, Level: 0})
		edges = kept
	}
	c := &model.Capture{
		Name: "i2c_sensor_bus", TimeUnit: "sample", SampleRate: rate(),
		Channels: []model.Channel{
			{ID: "ch0", Alias: "SCL-探头A", Role: "scl"},
			{ID: "ch1", Alias: "SDA-探头B", Role: "sda"},
		},
		Edges:  edges,
		Source: "casepack:logic-analyzer-export/i2c_sensor_bus.csv",
	}
	if variant == 1 {
		c.Name = "i2c_sensor_bus"
		c.ChangeNote = fmt.Sprintf(`{"region_start":%d,"region_end":%d,"what":"restore-missing-scl-edges"}`, regionStart, regionEnd)
	}
	return c, regionStart, regionEnd
}

// SPICapture: SCLK mode-0 signal, MOSI/MISO, two chip-selects (CS1 probe is
// inverted), one transfer with an internal clock pause (data gap).
func SPICapture() *model.Capture {
	sclk, mosi, miso, cs1, cs2 := "ch0", "ch1", "ch2", "ch3", "ch4"
	g := newGen(map[string]int{sclk: 0, mosi: 0, miso: 0, cs1: 0, cs2: 1})
	half := int64(6)
	spiByte := func(b byte) {
		for i := 7; i >= 0; i-- {
			g.set(mosi, int(b>>i)&1)
			g.wait(half)
			g.set(sclk, 1)
			g.wait(half)
			g.set(sclk, 0)
		}
	}
	g.wait(20)
	// device 1 (CS1 probe inverted: raw 1 during the active window)
	g.set(cs1, 1)
	g.wait(4)
	spiByte(0x9F)
	// clock pause inside CS window: data gap (80 > threshold 60)
	g.wait(80)
	spiByte(0x51)
	g.wait(4)
	g.set(cs1, 0)
	g.wait(30)
	// device 2
	g.set(cs2, 0)
	g.wait(4)
	spiByte(0x55)
	g.wait(4)
	g.set(cs2, 1)
	g.wait(20)
	return &model.Capture{
		Name: "spi_multi_device", TimeUnit: "sample", SampleRate: rate(),
		Channels: []model.Channel{
			{ID: "ch0", Alias: "SCLK", Role: "sclk"},
			{ID: "ch1", Alias: "MOSI", Role: "mosi"},
			{ID: "ch2", Alias: "MISO", Role: "miso"},
			{ID: "ch3", Alias: "CS1-反相探头", Role: "cs", Inverted: true},
			{ID: "ch4", Alias: "CS2", Role: "cs"},
		},
		Edges:  g.edges,
		Source: "casepack:logic-analyzer-export/spi_multi_device.csv",
	}
}

// SyncCapture: custom synchronous bus, data probe inverted (电平反相保留).
func SyncCapture() *model.Capture {
	clk, data := "ch0", "ch1"
	g := newGen(map[string]int{clk: 0, data: 1})
	half := int64(5)
	frame := func(b byte) {
		for i := 7; i >= 0; i-- {
			g.set(data, 1-(int(b>>i)&1)) // inverted probe: raw = NOT actual
			g.wait(half)
			g.set(clk, 1)
			g.wait(half)
			g.set(clk, 0)
		}
	}
	g.wait(15)
	frame(0xA5)
	g.wait(60) // frame gap (> threshold 50)
	frame(0x5A)
	g.wait(15)
	return &model.Capture{
		Name: "sync_custom", TimeUnit: "sample", SampleRate: rate(),
		Channels: []model.Channel{
			{ID: "ch0", Alias: "CLK-自定义", Role: "clock"},
			{ID: "ch1", Alias: "DATA-反相探头", Role: "data", Inverted: true},
		},
		Edges:  g.edges,
		Source: "casepack:logic-analyzer-export/sync_custom.csv",
	}
}

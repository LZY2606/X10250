// Package casepkg builds the offline domain case "边沿译码台：逻辑分析
// 波形、I2C/SPI 交易与重叠候选" and replays its event script: baseline
// import, decode, a parameter revision, a localized source amendment and
// conflicting human decisions.
package casepkg

import (
	"sort"
	"strconv"

	"edgebench/internal/core"
	"edgebench/internal/service"
	"edgebench/internal/store"
)

const us = 1e-6

type edgeList struct {
	edges []core.Edge
}

func (l *edgeList) add(ch string, tUs, level float64) {
	l.edges = append(l.edges, core.Edge{
		Channel: ch,
		Time:    core.Quantity{Raw: ftos(tUs) + "us", Unit: "s", Value: tUs * us, Basis: "time_seconds", OK: true},
		Level:   int(level),
	})
}

func ftos(f float64) string {
	return strconv.FormatFloat(f, 'f', 3, 64)
}

// i2c timing helpers: half-period 5us.
func (l *edgeList) i2cByte(t0 float64, value byte, ack bool) {
	for i := 0; i < 8; i++ {
		bit := (value >> uint(7-i)) & 1
		l.add("SCL", t0+float64(i)*10, 0)
		l.add("SDA", t0+float64(i)*10+1, float64(bit))
		l.add("SCL", t0+float64(i)*10+5, 1)
	}
	l.add("SCL", t0+80, 0)
	if ack {
		l.add("SDA", t0+81, 0)
	} else {
		l.add("SDA", t0+81, 1)
	}
	l.add("SCL", t0+85, 1)
}

// BuildCapture generates the baseline capture. stretchRise is the time
// of the SCL rising edge that ends the clock-stretching cell; the source
// amendment moves exactly this one edge.
func BuildCapture(name string, stretchRise float64) *core.Capture {
	l := &edgeList{}
	// Idle level establishment.
	l.add("SCL", -2, 1)
	l.add("SDA", -2, 1)
	l.add("CLK", -2, 0)
	l.add("MOSI", -2, 0)
	l.add("MISO", -2, 0)
	l.add("CS_EEPROM", -2, 1)
	l.add("CS_IMU", -2, 0) // inverted probe: raw low = interpreted idle high
	l.add("SYNCCLK", -2, 0)
	l.add("DATA", -2, 0)

	// ---- I2C transaction 1: addr 0x50 W, 0xA5, clock stretch, 0x3C NACK, repeated START.
	l.add("SDA", 0, 0) // START
	l.i2cByte(5, 0xA0, true)
	// byte 0xA5 starts after ack rise at 90: first fall at 95.
	l.i2cByte(95, 0xA5, true)
	// clock stretching before byte 3: SCL falls at 185, held low until stretchRise.
	l.add("SCL", 185, 0)
	l.add("SDA", 220, 0) // 0x3C bit0 = 0
	l.add("SCL", stretchRise, 1)
	// remaining bits of 0x3C (0111_100) at 10us cadence from stretchRise+10.
	base := stretchRise + 5            // next fall
	bits := []int{0, 1, 1, 1, 1, 0, 0} // bits 1..7 of 0x3C (bit0 already clocked)
	for i, b := range bits {
		fall := base + float64(i)*10
		l.add("SCL", fall, 0)
		l.add("SDA", fall+1, float64(b))
		l.add("SCL", fall+5, 1)
	}
	lastRise := base + 6*10 + 5
	// NACK bit.
	l.add("SCL", lastRise+5, 0)
	l.add("SDA", lastRise+6, 1)
	l.add("SCL", lastRise+10, 1)
	// repeated START: SCL high, SDA falls.
	sr := lastRise + 15
	l.add("SDA", sr, 0)

	// ---- I2C transaction 2 (after Sr): addr 0x50 R, 0x11 NACK, STOP.
	l.i2cByte(sr+5, 0xA1, true)
	t2 := sr + 5 + 90 // first fall of data byte
	l.i2cByte(t2, 0x11, false)
	stopBase := t2 + 90
	l.add("SCL", stopBase, 0)
	l.add("SDA", stopBase+1, 0)
	l.add("SCL", stopBase+5, 1)
	l.add("SDA", stopBase+10, 1) // STOP

	// ---- I2C transaction 3: data gap (no STOP, silence, no splice).
	l.add("SDA", 600, 0) // START
	l.i2cByte(605, 0xA0, true)
	l.add("SCL", 695, 0)
	l.add("SDA", 697, 1) // bus released, then 300us of silence
	l.add("SCL", 998, 1)
	l.add("SDA", 1000, 0) // new START after the gap
	l.i2cByte(1005, 0xA0, true)
	l.i2cByte(1095, 0x77, true)
	l.add("SCL", 1185, 0)
	l.add("SCL", 1190, 1)
	l.add("SDA", 1195, 1) // STOP

	// ---- I2C transaction 4: missing clock edge -> two legal candidates.
	l.add("SDA", 1200, 0) // START
	l.i2cByte(1205, 0xA0, true)
	l.i2cByte(1295, 0x55, true)
	// byte with a dropped clock pulse: rises at 1390,1400,1410,[missing],1435,...
	l.add("SCL", 1385, 0)
	l.add("SDA", 1387, 1) // bit0 = 1
	l.add("SCL", 1390, 1)
	l.add("SCL", 1395, 0)
	l.add("SDA", 1396, 0) // bits 1,2 = 0
	l.add("SCL", 1400, 1)
	l.add("SCL", 1405, 0)
	l.add("SCL", 1410, 1)
	l.add("SCL", 1415, 0)
	l.add("SDA", 1418, 1) // mid-cell SDA change: evidence of the missed clock
	l.add("SCL", 1430, 0)
	l.add("SCL", 1435, 1) // 25us after previous rise
	l.add("SCL", 1440, 0)
	l.add("SCL", 1445, 1)
	l.add("SCL", 1450, 0)
	l.add("SCL", 1455, 1)
	l.add("SCL", 1460, 0)
	l.add("SCL", 1465, 1)
	l.add("SDA", 1468, 0) // bit7 = 0
	l.add("SCL", 1470, 0)
	l.add("SCL", 1475, 1)
	l.add("SCL", 1480, 0)
	l.add("SCL", 1485, 1) // ACK (SDA low)
	l.add("SCL", 1490, 0)
	l.add("SCL", 1495, 1)
	l.add("SDA", 1500, 1) // STOP

	// ---- SPI device 1 (CS_EEPROM): 0xC3, 0x5A, clean sampling.
	l.add("CS_EEPROM", 2000, 0)
	spiByte := func(t0 float64, value byte, boundaryBit int) {
		for i := 0; i < 8; i++ {
			bit := (value >> uint(7-i)) & 1
			fall := t0 + float64(i)*2
			if i == boundaryBit {
				l.add("MOSI", fall+1.0+0.000002, float64(bit)) // 2ns after the sampling edge
			} else {
				l.add("MOSI", fall+0.2, float64(bit))
			}
			l.add("CLK", fall, 0)
			l.add("CLK", fall+1.0, 1)
		}
	}
	spiByte(2010, 0xC3, -1)
	spiByte(2026, 0x5A, -1)
	l.add("CS_EEPROM", 2045, 1)

	// ---- SPI device 2 (CS_IMU, inverted probe): 0x96, boundary sample on bit 4.
	l.add("CS_IMU", 2100, 1) // raw high = interpreted assert (inverted probe)
	spiByte(2110, 0x96, 4)
	l.add("CS_IMU", 2130, 0)

	// ---- Custom sync channel: 0x3C, 0xF0; DATA changes just after the
	// rising edge, so clock polarity decides the decoding.
	syncBits := []byte{0x3C, 0xF0}
	for bi, b := range syncBits {
		for i := 0; i < 8; i++ {
			bit := (b >> uint(7-i)) & 1
			rise := 3000 + float64(bi*8+i)
			l.add("DATA", rise+0.1, float64(bit))
			l.add("SYNCCLK", rise, 1)
			l.add("SYNCCLK", rise+0.5, 0)
		}
	}

	cap := &core.Capture{
		Name:       name,
		SampleRate: core.Quantity{Raw: "24MHz", Unit: "Hz", Value: 24e6, Basis: "frequency_hz", OK: true},
		Channels: []core.ChannelSpec{
			{Alias: "SDA", Role: "SDA"},
			{Alias: "SCL", Role: "SCL"},
			{Alias: "CLK", Role: "CLK"},
			{Alias: "MOSI", Role: "MOSI"},
			{Alias: "MISO", Role: "MISO"},
			{Alias: "CS_EEPROM", Role: "CS"},
			{Alias: "CS_IMU", Role: "CS", Inverted: true},
			{Alias: "SYNCCLK", Role: "SYNCCLK"},
			{Alias: "DATA", Role: "DATA"},
		},
		Edges: l.edges,
	}
	sort.SliceStable(cap.Edges, func(i, j int) bool {
		if cap.Edges[i].Time.Value != cap.Edges[j].Time.Value {
			return cap.Edges[i].Time.Value < cap.Edges[j].Time.Value
		}
		return cap.Edges[i].Channel < cap.Edges[j].Channel
	})
	return cap
}

// Load replays the full case script into the service. It is the offline
// case package shipped with the repository.
func Load(svc *service.Service) error {
	st := svc.St
	batch, err := st.BeginBatch()
	if err != nil {
		return err
	}
	if _, err := st.AppendEvent(batch, store.EvLoadCase, map[string]any{
		"case":    "边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选",
		"version": "case/1",
	}, nil); err != nil {
		return err
	}
	if err := st.CommitBatch(batch); err != nil {
		return err
	}

	// 1. Baseline import: edges, sample rate, aliases, inversion, clock
	// polarity and decoder version are all preserved as immutable material.
	cap := BuildCapture("LA-2210 基线采集", 225)
	if _, _, err := svc.ImportCapture(cap, store.EvImportCapture); err != nil {
		return err
	}
	// 2. Baseline decode.
	if _, err := svc.Decode(); err != nil {
		return err
	}
	// 3. Parameter revision: clock polarity of the sync channel flips.
	if _, err := svc.ReviseParams(func(p *core.Params) {
		p.ClockPolarity = "falling"
	}, "探头配置复核：同步通道时钟极性修正"); err != nil {
		return err
	}
	if _, err := svc.Decode(); err != nil {
		return err
	}
	// 4. Localized source amendment: re-time exactly one edge (the
	// clock-stretch release), derived from the baseline capture.
	amended := BuildCapture("LA-2210 基线采集 (时基校准)", 228)
	amended.ParentID = cap.ID
	if _, _, err := svc.AmendSource(amended); err != nil {
		return err
	}
	if _, err := svc.Decode(); err != nil {
		return err
	}
	// 5. Conflicting human decisions on the missing-clock ambiguity.
	resID, err := st.LatestResultID(0)
	if err != nil {
		return err
	}
	res, err := st.GetResult(resID)
	if err != nil {
		return err
	}
	for _, amb := range res.Ambiguities {
		if amb.Reason != core.DiagMissingClockEdge || len(amb.Candidates) < 2 {
			continue
		}
		if _, err := svc.Decide(store.Decision{
			Action: "accept", Ambiguity: amb.ID, Candidate: amb.Candidates[0].ID,
			Reviewer: "li.wei", Note: "示波器复测未见缺边，采纳观测假设",
		}); err != nil {
			return err
		}
		if _, err := svc.Decide(store.Decision{
			Action: "accept", Ambiguity: amb.ID, Candidate: amb.Candidates[1].ID,
			Reviewer: "sato", Note: "采样率余量不足，采纳缺边假设",
		}); err != nil {
			return err
		}
	}
	return nil
}

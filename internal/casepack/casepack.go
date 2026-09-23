// Package casepack ships the offline domain case "edge decoding workbench:
// logic-analyzer waveforms, I2C/SPI transactions and overlapping candidates".
// It builds the immutable capture, the initial params, and replays the full
// narrative: baseline -> parameter revision -> localized source change ->
// conflicting human decisions.
package casepack

import (
	"fmt"

	"edgeworkbench/internal/model"
)

const (
	CaseName       = "edge-decode-bench/v1"
	SampleRateHz   = 100_000_000 // 100 MHz -> 10 ns sample period
	DecoderVersion = "1.0.0"
	halfI2C        = 500 // ns, half SCL period
	halfSPI        = 250 // ns, half SCLK period
)

type wavegen struct {
	edges []model.Edge
}

func (w *wavegen) add(ch string, t int64, lvl int) {
	w.edges = append(w.edges, model.Edge{
		Channel: ch, TimeNs: t, Level: lvl,
		Raw: fmt.Sprintf("%s=%d@%dns", ch, lvl, t),
	})
}

// i2cStart emits START; returns time with SCL low.
func (w *wavegen) i2cStart(t int64) int64 {
	w.add("SDA", t, 0)
	w.add("SCL", t+halfI2C, 0)
	return t + halfI2C
}

// i2cByte emits 8 data bits + ack bit; SCL low on entry and exit.
func (w *wavegen) i2cByte(t int64, val, ack int, stretchNs int64, boundaryBit int, skipAckClock bool) int64 {
	return w.i2cByteFix(t, val, ack, stretchNs, boundaryBit, skipAckClock, false)
}

func (w *wavegen) i2cByteFix(t int64, val, ack int, stretchNs int64, boundaryBit int, skipAckClock, restoreClock bool) int64 {
	t += stretchNs // SCL held low: clock stretching
	for i := 7; i >= 0; i-- {
		bit := (val >> i) & 1
		if i == boundaryBit {
			// boundary sample: SDA toggles exactly at the SCL rising edge
			w.add("SCL", t+halfI2C, 1)
			w.add("SDA", t+halfI2C, bit)
		} else {
			w.add("SDA", t, bit)
			w.add("SCL", t+halfI2C, 1)
		}
		w.add("SCL", t+2*halfI2C, 0)
		t += 2 * halfI2C
	}
	if skipAckClock && !restoreClock {
		// missing clock edge: the 9th SCL pulse never arrives
		w.add("SDA", t, ack)
		return t
	}
	w.add("SDA", t, ack)
	w.add("SCL", t+halfI2C, 1)
	w.add("SCL", t+2*halfI2C, 0)
	return t + 2*halfI2C
}

func (w *wavegen) i2cRepeatedStart(t int64) int64 {
	w.add("SDA", t, 1)
	w.add("SCL", t+halfI2C, 1)
	w.add("SDA", t+2*halfI2C, 0)
	w.add("SCL", t+3*halfI2C, 0)
	return t + 3*halfI2C
}

func (w *wavegen) i2cStop(t int64) int64 {
	w.add("SDA", t, 0)
	w.add("SCL", t+halfI2C, 1)
	w.add("SDA", t+2*halfI2C, 1)
	return t + 2*halfI2C
}

// spiFrame emits one CS-framed SPI transfer in mode-0 convention: MOSI
// advances exactly on the SCLK falling edge. Sampling on rising edges
// (mode 0) is clean; sampling on falling edges (mode 3) hits a boundary
// hazard on every bit, which is exactly why an unknown SPI mode must keep
// parallel candidates with different confidence.
func (w *wavegen) spiFrame(t int64, cs string, bytes ...int) int64 {
	w.add(cs, t, 0)
	t += halfSPI
	for _, b := range bytes {
		for i := 7; i >= 0; i-- {
			w.add("MOSI", t, (b>>i)&1)
			w.add("SCLK", t+halfSPI, 1)
			w.add("SCLK", t+2*halfSPI, 0)
			t += 2 * halfSPI
		}
	}
	w.add(cs, t+halfSPI, 1)
	return t + 2*halfSPI
}

func (w *wavegen) syncFrame(t int64, val int) int64 {
	w.add("EN", t, 1)
	w.add("DAT", t+100, (val>>7)&1)
	t += 200
	for i := 7; i >= 0; i-- {
		w.add("CLK", t, 1)                  // rising edge: DATA holds current bit
		w.add("DAT", t+50, nextBit(val, i)) // DATA moves shortly after rising
		w.add("CLK", t+200, 0)              // falling edge: DATA already advanced
		t += 400
	}
	w.add("EN", t, 0)
	return t
}

func nextBit(val, i int) int {
	if i == 0 {
		return 0
	}
	return (val >> (i - 1)) & 1
}

// BuildCaptureV1 builds the immutable baseline material.
func BuildCaptureV1() *model.Capture {
	return buildWave(false, "")
}

// BuildCaptureV2 is the localized source change: only the missing 9th SCL
// edge of the third I2C transaction is restored. Everything else is
// bit-identical, so dependent re-derivation stays local.
func BuildCaptureV2(parentID string) *model.Capture {
	return buildWave(true, parentID)
}

func buildWave(fixMissingClock bool, parentID string) *model.Capture {
	w := &wavegen{}

	// --- I2C txn 1 @10us: write 0xA5, clock stretch, write 0x11,
	//     repeated START, read 0x3C NACK, STOP ---
	t := w.i2cStart(10_000)
	t = w.i2cByte(t, 0xA0, 0, 0, -1, false)      // addr 0x50 write, ACK
	t = w.i2cByte(t, 0xA5, 0, 0, -1, false)      // data, ACK
	t = w.i2cByte(t, 0x11, 0, 10_000, -1, false) // 10us clock stretch before byte
	t = w.i2cRepeatedStart(t)
	t = w.i2cByte(t, 0xA1, 0, 0, -1, false) // addr 0x50 read, ACK
	t = w.i2cByte(t, 0x3C, 1, 0, -1, false) // data, NACK
	t = w.i2cStop(t)

	// --- I2C txn 2 @200us: addr, then a 300us data gap mid-transaction;
	//     trailing clocks are orphan and must not be stitched ---
	t = w.i2cStart(200_000)
	t = w.i2cByte(t, 0xA0, 0, 0, -1, false)
	t += 300_000                            // data gap: no edges at all
	t = w.i2cByte(t, 0x77, 0, 0, -1, false) // orphan fragment, no START
	w.add("SCL", t+500, 1)                  // bus recovers to idle
	w.add("SDA", t+500, 1)

	// --- I2C txn 3 @700us: 9th clock edge missing, transaction dies in a
	//     300us data gap; the dangling bits must fork candidates ---
	t = w.i2cStart(700_000)
	t = w.i2cByte(t, 0xA4, 0, 0, -1, false)                    // addr 0x52 write, ACK
	t = w.i2cByteFix(t, 0x55, 0, 0, -1, true, fixMissingClock) // 9th clock missing (restored in v2)
	t += 300_000                                               // data gap: no edges at all, transaction dies here
	w.add("SCL", t, 1)                                         // bus recovery after the gap (orphan clock, not data)
	w.add("SDA", t+500, 1)

	// --- I2C txn 4 @1.1ms: boundary sample on bit 3 ---
	t = w.i2cStart(1_100_000)
	t = w.i2cByte(t, 0xA4, 0, 0, -1, false) // addr 0x52 write, ACK
	t = w.i2cByte(t, 0xB1, 0, 0, 3, false)  // bit 3 toggles exactly at SCL rise
	t = w.i2cStop(t)

	// --- SPI: two devices on separate chip selects ---
	t = w.spiFrame(1_200_000, "CS0", 0x9E, 0x01)
	t = w.spiFrame(1_500_000, "CS1", 0x42)

	// --- custom sync bus: one framed byte ---
	w.syncFrame(2_000_000, 0xB2)

	c := &model.Capture{
		Name:         CaseName,
		SampleRateHz: SampleRateHz,
		Channels: []model.Channel{
			{Alias: "SCL", Role: "scl"},
			{Alias: "SDA", Role: "sda"},
			{Alias: "SCLK", Role: "sclk"},
			{Alias: "MOSI", Role: "mosi"},
			{Alias: "MISO", Role: "miso"},
			{Alias: "CS0", Role: "cs"},
			{Alias: "CS1", Role: "cs"},
			{Alias: "CLK", Role: "clk"},
			{Alias: "DAT", Role: "data"},
			{Alias: "EN", Role: "en"},
		},
		Edges: w.edges,
		Note:  "baseline: raw edge times, sample rate, aliases, inversion, clock polarity and decoder version preserved",
	}
	if fixMissingClock {
		c.Note = "localized source change: missing 9th SCL clock edge of I2C txn @700us restored"
	}
	c.ID = model.ComputeCaptureID(c.Name, c.SampleRateHz, c.Channels, c.Edges, parentID)
	c.ParentID = parentID
	c.AssignIDs()
	return c
}

// BuildParamsV1 is the initial probe/decoder configuration.
func BuildParamsV1() *model.Params {
	p := &model.Params{
		Label:               "v1 baseline probe config",
		ClockPolarity:       "rising",
		SPIMode:             "unknown",
		GapThresholdNs:      100_000,
		StretchThresholdNs:  5_000,
		DecoderVersion:      DecoderVersion,
		BudgetMaxEdges:      100_000,
		BudgetMaxCandidates: 32,
	}
	p.ID = model.ComputeParamsID(*p)
	return p
}

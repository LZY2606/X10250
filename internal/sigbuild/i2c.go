package sigbuild

// I2C timing plan (one SCL period = 2000 ns, quarters = 500 ns). The exact
// numbers are evidence used by the boundary-sampling and stretching tests.
const (
	I2CQtr   = 500
	I2CHalf  = 1000
	I2CPhase = 2000
	// I2CGap is a long SDA/SCL-idle hole between two byte groups.
	I2CGap = 40000
)

// I2CBuilder wraps Builder with bus-shaped primitives.
type I2CBuilder struct {
	B                 *Builder
	SCL, SDA          string
	period, half, qtr float64
}

// NewI2C returns an I2C builder with SCL/SDA channels (configurable aliases,
// inversion and raw clock-polarity label).
func NewI2C(cfg Config, sclAlias, sdaAlias string, invertedSCL bool, clkPol string) *I2CBuilder {
	b := New(cfg,
		ChanSpec{ID: "ch0", Alias: sclAlias, Role: "scl", Inverted: invertedSCL,
			ClockPolarity: clkPol, StartLevel: 1},
		ChanSpec{ID: "ch1", Alias: sdaAlias, Role: "sda", StartLevel: 1},
	)
	return &I2CBuilder{B: b, SCL: "ch0", SDA: "ch1",
		period: I2CPhase, half: I2CHalf, qtr: I2CQtr}
}

// Start emits a START: SDA falls while SCL is high.
func (x *I2CBuilder) Start() {
	x.B.set(x.SDA, 0)
	x.B.Advance(x.qtr)
	x.B.set(x.SCL, 0)
	x.B.Advance(x.half - x.qtr)
}

// RepeatedStart emits STOP-less (re)START: SCL goes high then SDA falls.
func (x *I2CBuilder) RepeatedStart() {
	x.B.set(x.SDA, 1)
	x.B.Advance(x.qtr)
	x.B.set(x.SCL, 1)
	x.B.Advance(x.qtr)
	x.B.set(x.SDA, 0)
	x.B.Advance(x.qtr)
	x.B.set(x.SCL, 0)
	x.B.Advance(x.half - x.qtr)
}

// Stop emits a STOP: SDA rises while SCL is high.
func (x *I2CBuilder) Stop() {
	x.B.set(x.SDA, 0)
	x.B.Advance(x.qtr)
	x.B.set(x.SCL, 1)
	x.B.Advance(x.qtr)
	x.B.set(x.SDA, 1)
	x.B.Advance(x.half)
}

// Byte emits one 8-bit byte MSB-first and one ACK bit. ack=true means the
// receiver drove ACK (SDA low during the 9th pulse). Returns the time window
// [start,end] of the byte in ns.
func (x *I2CBuilder) Byte(v int, ack bool) (float64, float64) {
	start := x.B.T()
	for bit := 7; bit >= 0; bit-- {
		x.B.set(x.SDA, (v>>uint(bit))&1)
		x.B.Advance(x.qtr)
		x.B.set(x.SCL, 1)
		x.B.Advance(x.half)
		x.B.set(x.SCL, 0)
		x.B.Advance(x.qtr)
	}
	// ACK bit: master releases SDA (high); receiver holds low on ACK.
	x.B.set(x.SDA, ackChoice(ack))
	x.B.Advance(x.qtr)
	x.B.set(x.SCL, 1)
	x.B.Advance(x.half)
	x.B.set(x.SCL, 0)
	x.B.Advance(x.qtr)
	return start, x.B.T()
}

func ackChoice(ack bool) int {
	if ack {
		return 0
	}
	return 1
}

// Stretch holds SCL low for extra ns between two pulses (clock stretching).
func (x *I2CBuilder) Stretch(extraNS float64) {
	x.B.set(x.SCL, 0)
	x.B.Advance(extraNS)
}

// Gap leaves the bus idle for ns (data gap evidence; must not be joined).
func (x *I2CBuilder) Gap(ns float64) {
	x.B.set(x.SCL, 1)
	x.B.set(x.SDA, 1)
	x.B.Advance(ns)
}

// PartialByte emits bits 7..bitStop (inclusive) then stops without a 9th pulse
// and without STOP - the material for "missing clock edge" evidence.
func (x *I2CBuilder) PartialByte(v int, bitsEmitted int) (float64, float64) {
	start := x.B.T()
	for bit := 7; bit > 7-bitsEmitted; bit-- {
		x.B.set(x.SDA, (v>>uint(bit))&1)
		x.B.Advance(x.qtr)
		x.B.set(x.SCL, 1)
		x.B.Advance(x.half)
		x.B.set(x.SCL, 0)
		x.B.Advance(x.qtr)
	}
	return start, x.B.T()
}

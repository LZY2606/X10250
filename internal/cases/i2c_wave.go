package cases

// I2C timing: SCL period 2500 ns (low 1250, high 1250). SDA changes while
// SCL is low; rising SCL is the sample point.

type i2cWriter struct {
	b             *edgeBuilder
	t             int64
	sda, scl      string
	sdaLevel      int
	sclLevel      int
	stretchBudget int64
}

const (
	i2cHalf    int64 = 1250
	i2cPeriod  int64 = 2500
	i2cStretch int64 = 4000
)

func newI2C(b *edgeBuilder, t int64, scl, sda string) *i2cWriter {
	w := &i2cWriter{b: b, t: t, sda: sda, scl: scl, sdaLevel: 1, sclLevel: 1,
		stretchBudget: i2cStretch}
	return w
}

func (w *i2cWriter) setSDA(lvl int) {
	if w.sdaLevel != lvl {
		w.b.at(w.sda, w.t, lvl)
		w.sdaLevel = lvl
	}
}

func (w *i2cWriter) setSCL(lvl int) {
	if w.sclLevel != lvl {
		w.b.at(w.scl, w.t, lvl)
		w.sclLevel = lvl
	}
}

func (w *i2cWriter) start() {
	// Both high (idle); SDA falls while SCL stays high.
	w.setSCL(1)
	w.setSDA(1)
	w.t += 200
	w.setSDA(0) // START
	w.t += 200
}

func (w *i2cWriter) repeatedStart() {
	// Last bit ended with SCL high. Release SDA during the next low, then
	// bring SCL high and pull SDA low: a START marker without any STOP.
	w.setSCL(0)
	w.t += 100
	w.setSDA(1)
	w.t += i2cHalf - 100
	w.setSCL(1)
	w.t += 200
	w.setSDA(0)
	w.t += 200
}

func (w *i2cWriter) bit(value, stretched int64) {
	low := i2cHalf
	w.setSCL(0)
	w.t += 150
	w.setSDA(int(value))
	if stretched == 1 {
		low = 5200 // SCL held low well beyond the stretch budget
	}
	w.t += low - 150
	w.setSCL(1) // sample point
	if stretched == 1 {
		// Return SDA driver timing remains; clock low is the stretched part.
	}
	w.t += i2cPeriod - low
}

func (w *i2cWriter) byte(val int, ack int, stretchBit int) {
	for k := 7; k >= 0; k-- {
		sb := 0
		if k == stretchBit {
			sb = 1
		}
		w.bit(int64((val>>k)&1), int64(sb))
	}
	w.bit(int64(ack), 0) // ACK(0) / NACK(1) driven by slave
}

func (w *i2cWriter) partial(bits int) {
	for k := 0; k < bits; k++ {
		w.bit(int64(k%2), 0)
	}
}

func (w *i2cWriter) stop() {
	w.setSCL(0)
	w.t += 100
	w.setSDA(0)
	w.t += i2cHalf - 100
	w.setSCL(1)
	w.t += 200
	w.setSDA(1) // STOP
	w.t += 400
}

func (w *i2cWriter) now() int64 { return w.t }

// buildI2C writes two transactions. The first uses repeated START, clock
// stretching and a NACK; after a long silence the second is a truncated
// byte (missing clock edges), so multiple legal candidates survive.
func buildI2C(b *edgeBuilder) {
	w := newI2C(b, 10_000, "SCL", "SDA")
	w.start()
	w.byte(0xA0, 0, 3) // address 0x50 + W, ACK, stretch on bit 3
	w.byte(0x11, 0, -1)
	w.repeatedStart()
	w.byte(0xA1, 0, -1) // address 0x50 + R
	w.byte(0x22, 1, -1) // data, NACK
	w.stop()

	// Data gap: advance well beyond the auto gap limit (~30 us).
	w.t += 45_000
	w.start()
	w.partial(5) // only five clocks of a byte: incomplete frame
	w.stop()
}

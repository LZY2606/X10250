package cases

type spiWriter struct {
	b            *edgeBuilder
	t            int64
	sclk, mosi   string
	cs           string
	clk, mosiLvl int
	half         int64
}

func newSPI(b *edgeBuilder, t int64, sclk, mosi, cs string, cpol int) *spiWriter {
	w := &spiWriter{b: b, t: t, sclk: sclk, mosi: mosi, cs: cs,
		clk: cpol, mosiLvl: 0, half: 50}
	w.b.at(sclk, t, cpol)
	return w
}

func (w *spiWriter) selectCS() {
	w.b.at(w.cs, w.t, 0)
	w.t += 60
}

func (w *spiWriter) releaseCS() {
	w.t += 60
	w.b.at(w.cs, w.t, 1)
}

func (w *spiWriter) setMOSI(v int) {
	if w.mosiLvl != v {
		w.b.at(w.mosi, w.t, v)
		w.mosiLvl = v
	}
}

func (w *spiWriter) bit(v int) {
	// Leading edge sample (mode0): data valid, clock rises, then falls.
	w.t += 20
	w.setMOSI(v)
	w.t += 30
	w.b.at(w.sclk, w.t, w.clk^1)
	w.t += w.half
	w.b.at(w.sclk, w.t, w.clk)
	w.t += w.half - 50
}

func (w *spiWriter) byteVal(val int) {
	for k := 7; k >= 0; k-- {
		w.bit((val >> k) & 1)
	}
}

func (w *spiWriter) pause(ns int64) { w.t += ns }

func buildSPI(b *edgeBuilder, start int64) int64 {
	// Device A: CS0, complete 2-byte frame.
	w := newSPI(b, start, "SCK", "MOSI", "CS0", 0)
	w.selectCS()
	w.byteVal(0xC3)
	w.byteVal(0x7E)
	w.releaseCS()
	w.pause(400)

	// Device B: CS1 shares SCK/MOSI (multi-device chip select) and its frame
	// ends on an odd clock edge -> missing-edge evidence.
	w2 := &spiWriter{b: b, t: w.t + 300, sclk: "SCK", mosi: "MOSI", cs: "CS1",
		clk: 0, mosiLvl: w.mosiLvl, half: 50}
	w2.selectCS()
	w2.byteVal(0x5A)
	w2.bit(1) // one extra bit: odd trailing half-cycle
	// Force an unmatched edge by toggling clock once more without returning.
	w2.pause(10)
	w2.b.at("SCK", w2.t, 1)
	w2.pause(50)
	w2.releaseCS()
	w.pause(300)

	// Device A again, but with a clock silence inside CS that exceeds the gap
	// limit (~1.6 us): the frame must split rather than stitch.
	w3 := &spiWriter{b: b, t: w2.t + 300, sclk: "SCK", mosi: "MOSI", cs: "CS0",
		clk: 0, mosiLvl: w2.mosiLvl, half: 50}
	w3.selectCS()
	w3.byteVal(0x0F)
	w3.pause(5000)
	w3.byteVal(0xF0)
	w3.releaseCS()
	return w3.t + 500
}

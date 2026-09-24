package cases

// buildCustom writes a free-running custom synchronous stream (no CS),
// sampled on the leading clock edge in MSB order, followed by a gap and a
// second word.
func buildCustom(b *edgeBuilder, start int64) int64 {
	t := start
	b.at("CCK", t, 0)
	t += 100
	clkLvl, mosiLvl := 0, 0
	pulseBit := func(v int) {
		t += 20
		if mosiLvl != v {
			b.at("CDAT", t, v)
			mosiLvl = v
		}
		t += 30
		b.at("CCK", t, clkLvl^1)
		t += 50
		b.at("CCK", t, clkLvl)
		t += 50
	}
	putByte := func(val int) {
		for k := 7; k >= 0; k-- {
			pulseBit((val >> k) & 1)
		}
	}
	putByte(0x9A)
	putByte(0x6B)
	t += 6000 // silence beyond gap limit -> separate transaction
	putByte(0x12)
	return t + 400
}

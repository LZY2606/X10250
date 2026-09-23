package decode

import "fmt"

// SPI / custom-sync findings:
//   spi.cs_begin/end       info    chip-select transaction delimiter
//   spi.data_gap           error   gap during CS-low terminates a transaction
//   spi.missing_clock_edge error   dangling active clock edge / partial byte
//   spi.boundary_sample    warning data changed while opposite clock state active
//   spi.mode_ambiguous     info    SPI mode unknown; all legal candidates kept
//   sync.word              info    custom sync word observed
//   sync.no_sync           error   no legal sync alignment found

type spiBit struct {
	activeEdge levelEvent
	opposite   levelEvent
	mosi       int
	miso       int
	boundary   bool
	hasActive  bool
	hasOpp     bool
}

type spiFrame struct {
	bits       []spiBit
	csRole     string
	device     string
	start, end int64
	gapClosed  bool
}

type spiScanner struct {
	st       *edgeStream
	frames   []spiFrame
	active   map[string]*spiFrame
	lastEdge map[string]int64
	findings []Finding
}

func decodeSPI(st *edgeStream, res *DecodeResult) error {
	if st.roleCh["sclk"] == nil {
		return &SemanticError{Diagnostics: []Diagnostic{{
			Code: "missing_role_channel", Path: "channels",
			Message: "spi capture requires a channel with role sclk", Severity: "error",
		}}}
	}
	if len(st.csRoles()) == 0 {
		return &SemanticError{Diagnostics: []Diagnostic{{
			Code: "missing_role_channel", Path: "channels",
			Message: "spi capture requires at least one channel with role cs0/cs1/...", Severity: "error",
		}}}
	}
	return runSerialScan(st, res, "spi")
}

func decodeSync(st *edgeStream, res *DecodeResult) error {
	if st.roleCh["clk"] == nil || st.roleCh["data"] == nil {
		return &SemanticError{Diagnostics: []Diagnostic{{
			Code: "missing_role_channel", Path: "channels",
			Message: "sync capture requires channels with roles clk and data", Severity: "error",
		}}}
	}
	return runSerialScan(st, res, "sync")
}

func (s *edgeStream) csRoles() []string {
	var out []string
	for r := range s.roleCh {
		if len(r) >= 2 && r[:2] == "cs" {
			out = append(out, r)
		}
	}
	sortStrings(out)
	return out
}

func sortStrings(a []string) {
	for i := 0; i < len(a); i++ {
		for j := i + 1; j < len(a); j++ {
			if a[j] < a[i] {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}

func runSerialScan(st *edgeStream, res *DecodeResult, proto string) error {
	sc := &spiScanner{st: st, active: map[string]*spiFrame{}, lastEdge: map[string]int64{}}
	gap := st.cap.Params.GapNS
	if gap <= 0 {
		gap = 50_000
	}
	clkRole := map[string]bool{"sclk": true, "clk": true}
	dataRoles := map[string]bool{"mosi": true, "miso": true, "data": true}

	for i := range st.evs {
		ev := &st.evs[i]
		if !ev.valid {
			continue
		}
		if isCSRole(ev.role) {
			sc.onCS(ev, proto)
			continue
		}
		if clkRole[ev.role] {
			sc.onClock(ev, proto, gap)
			continue
		}
		if dataRoles[ev.role] {
			sc.onData(ev)
		}
	}
	sc.terminateDangling()
	res.Transactions = sc.build(proto)
	res.Findings = append(res.Findings, sc.findings...)
	return nil
}

func isCSRole(r string) bool { return len(r) >= 2 && r[:2] == "cs" }

func (sc *spiScanner) activeLevel(csRole string) int {
	ch := sc.st.roleCh[csRole]
	if ch != nil && sc.st.cap.Params.ActiveLevel == "high" {
		return 1
	}
	return 0
}

func (sc *spiScanner) onCS(ev *levelEvent, proto string) {
	active := ev.logic == sc.activeLevel(ev.role)
	if active {
		if _, exists := sc.active[ev.role]; exists {
			return
		}
		f := &spiFrame{csRole: ev.role, device: ev.role, start: ev.t, end: ev.t}
		sc.active[ev.role] = f
		sc.lastEdge[ev.role] = ev.t
		sc.addFinding(proto+".cs_begin", "info", ev.t, ev.t, ev.role,
			proto+" transaction began (chip select "+ev.role+" active)", ev.edge.ID, "")
	} else {
		f := sc.active[ev.role]
		if f == nil {
			return
		}
		f.end = ev.t
		sc.frames = append(sc.frames, *f)
		delete(sc.active, ev.role)
		sc.addFinding(proto+".cs_end", "info", ev.t, ev.t, ev.role,
			proto+" transaction ended (chip select "+ev.role+" inactive)", ev.edge.ID, "")
	}
}

func (sc *spiScanner) onClock(ev *levelEvent, proto string, gap int64) {
	cs := sc.clockCS(ev)
	if cs == "" {
		return
	}
	f := sc.active[cs]
	if f == nil {
		return
	}
	if last := sc.lastEdge[cs]; last > 0 && ev.t-last > gap && len(f.bits) > 0 {
		// data gap while CS active: terminate; do not stitch into next frame
		f.gapClosed = true
		f.end = last
		sc.addFinding(proto+".data_gap", "error", last, ev.t, ev.role,
			fmt.Sprintf("no clock for %d ns (> gap %d ns) while select active; not stitched", ev.t-last, gap),
			ev.edge.ID, "")
		sc.frames = append(sc.frames, *f)
		nf := &spiFrame{csRole: cs, device: cs, start: ev.t, end: ev.t}
		sc.active[cs] = nf
		f = nf
	}
	cpol := 0
	if sc.st.cap.Params.ClockPolarity == "high" {
		cpol = 1
	}
	cpha := 0
	if sc.st.cap.Params.ClockPhase == "shift" {
		cpha = 1
	}
	isActive := ev.logic == (cpol ^ 1)
	if isActive {
		f.bits = append(f.bits, spiBit{
			activeEdge: *ev, hasActive: true,
			mosi: sc.sample("mosi", "data", ev.t, cpol, cpha),
			miso: sc.sample("miso", "", ev.t, cpol, cpha),
		})
	} else if len(f.bits) > 0 {
		b := &f.bits[len(f.bits)-1]
		b.opposite = *ev
		b.hasOpp = true
	} else {
		sc.addFinding(proto+".missing_clock_edge", "warning", ev.t, ev.t, ev.role,
			"inactive clock edge before any sample edge (possible missing leading edge)", ev.edge.ID, "")
	}
	f.end = ev.t
	sc.lastEdge[cs] = ev.t
}

func (sc *spiScanner) sample(role, alt string, t int64, cpol, cpha int) int {
	r := role
	if sc.st.roleCh[r] == nil && alt != "" {
		r = alt
	}
	if sc.st.roleCh[r] == nil {
		return 0
	}
	return sc.st.levelAt(r, t)
}

func (sc *spiScanner) clockCS(ev *levelEvent) string {
	for cs := range sc.active {
		return cs
	}
	return ""
}

func (sc *spiScanner) onData(ev *levelEvent) {
	for cs, f := range sc.active {
		if len(f.bits) == 0 {
			continue
		}
		last := &f.bits[len(f.bits)-1]
		if !last.hasActive {
			continue
		}
		clkLevel := sc.st.levelAt("sclk", ev.t)
		if sc.st.roleCh["sclk"] == nil {
			clkLevel = sc.st.levelAt("clk", ev.t)
		}
		// a data change while clock is at the sampling state is a boundary risk
		cpol := 0
		if sc.st.cap.Params.ClockPolarity == "high" {
			cpol = 1
		}
		if clkLevel == (cpol ^ 1) {
			last.boundary = true
			sc.addFinding("spi.boundary_sample", "warning", ev.t, ev.t, ev.role,
				"data changed while clock was at the active/sampling level", ev.edge.ID, "")
		}
		_ = cs
	}
}

func (sc *spiScanner) terminateDangling() {
	for cs, f := range sc.active {
		if len(f.bits) > 0 && !f.bits[len(f.bits)-1].hasOpp {
			sc.addFinding("spi.missing_clock_edge", "error",
				f.bits[len(f.bits)-1].activeEdge.t, f.end, "sclk",
				"active clock edge without a return edge before CS closed/end",
				f.bits[len(f.bits)-1].activeEdge.edge.ID, "")
		}
		if len(f.bits)%8 != 0 {
			sc.addFinding("spi.missing_clock_edge", "error", f.start, f.end, cs,
				fmt.Sprintf("frame collected %d bits, not an integer byte", len(f.bits)),
				"", "")
		}
		sc.frames = append(sc.frames, *f)
		delete(sc.active, cs)
	}
}

type serialMode struct {
	name      string
	cpol, cph int
	sampleOpp bool // when true the sample value is taken at the opposite edge
}

func spiModes(known string) []serialMode {
	switch known {
	case "0":
		return []serialMode{{"mode0(CPOL=0,CPHA=0)", 0, 0, false}}
	case "1":
		return []serialMode{{"mode1(CPOL=0,CPHA=1)", 0, 1, true}}
	case "2":
		return []serialMode{{"mode2(CPOL=1,CPHA=0)", 1, 0, false}}
	case "3":
		return []serialMode{{"mode3(CPOL=1,CPHA=1)", 1, 1, true}}
	}
	return []serialMode{
		{"mode0(CPOL=0,CPHA=0)", 0, 0, false},
		{"mode1(CPOL=0,CPHA=1)", 0, 1, true},
		{"mode2(CPOL=1,CPHA=0)", 1, 0, false},
		{"mode3(CPOL=1,CPHA=1)", 1, 1, true},
	}
}

func (sc *spiScanner) addFinding(code, sev string, s, e int64, ch, detail, edgeID, candID string) {
	sc.findings = append(sc.findings, Finding{
		ID:   hashNS("fd_", sc.st.cap.ID, code, ch, itoa(int(s)), itoa(int(e)), detail),
		Code: code, Kind: "state", Severity: sev, StartNS: s, EndNS: e, Channel: ch,
		Detail: detail, RefEdge: edgeID, RefCand: candID,
	})
}

func (sc *spiScanner) build(proto string) []Transaction {
	var out []Transaction
	capID := sc.st.cap.ID
	knownMode := sc.st.cap.Params.SpiMode
	if proto == "sync" {
		knownMode = sc.st.cap.Params.ClockPolarity
	}
	modes := spiModes(knownMode)
	for fi, fr := range sc.frames {
		var cands []Candidate
		edgeIDs := []string{}
		for _, b := range fr.bits {
			if b.hasActive {
				edgeIDs = append(edgeIDs, b.activeEdge.edge.ID)
			}
			if b.hasOpp {
				edgeIDs = append(edgeIDs, b.opposite.edge.ID)
			}
		}
		edgeIDs = uniqEdgeIDs(edgeIDs)
		for ci, m := range modes {
			bytes := sc.assembleBytes(&fr, m, proto, fi, ci)
			conf := 1.0
			note := ""
			if len(modes) > 1 {
				conf = 1.0 / float64(len(modes))
				note = proto + " mode unknown: all four legal clock mode candidates retained"
			}
			cid := hashNS("cd_", capID, proto, itoa(fi), m.name, sc.st.cap.DecoderVer)
			cands = append(cands, Candidate{
				ID: cid, Index: ci, DecoderVer: sc.st.cap.DecoderVer, Mode: m.name,
				Confidence: round1(conf), Bytes: bytes, Note: note, EdgeIDs: edgeIDs,
			})
		}
		if len(modes) > 1 {
			sc.addFindingOnce(proto+".mode_ambiguous", "info", fr.start, fr.end, "sclk",
				"clock mode not uniquely determined; legal candidates are retained together")
		}
		status := "complete"
		if fr.gapClosed || len(fr.bits)%8 != 0 {
			status = "incomplete"
		}
		if proto == "sync" {
			sc.annotateSync(&fr, cands)
		}
		tid := hashNS("tx_", capID, proto, fr.csRole, itoa(int(fr.start)), itoa(int(fr.end)))
		out = append(out, Transaction{
			ID: tid, Index: fi, Protocol: proto, Device: fr.device,
			StartNS: fr.start, EndNS: fr.end, Status: status, Segments: 1,
			Candidate: cands, EdgeIDs: edgeIDs,
		})
	}
	return out
}

func round1(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}

func (sc *spiScanner) addFindingOnce(code, sev string, s, e int64, ch, detail string) {
	id := hashNS("fd_", sc.st.cap.ID, code, ch, itoa(int(s)), itoa(int(e)), detail)
	for _, f := range sc.findings {
		if f.ID == id {
			return
		}
	}
	sc.findings = append(sc.findings, Finding{
		ID: id, Code: code, Kind: "state", Severity: sev, StartNS: s, EndNS: e,
		Channel: ch, Detail: detail,
	})
}

func (sc *spiScanner) assembleBytes(fr *spiFrame, m serialMode, proto string, fi, ci int) []Byte {
	var out []Byte
	msbFirst := sc.st.cap.Params.BitOrder != "lsb"
	n := len(fr.bits)
	// CPHA=1 shifts on the leading edge and samples on the trailing edge; the
	// stored spiBit sequence is keyed on "active" edges, so for shift-phase
	// modes the sampled value is the data level at the opposite edge. We model
	// that by taking the value captured at the next bit's active edge window.
	sampleAt := make([]int, n)
	for i := 0; i < n; i++ {
		b := fr.bits[i]
		if m.sampleOpp {
			// data level at opposite edge: query the data channel then.
			r := "mosi"
			if proto == "sync" {
				r = "data"
			}
			if sc.st.roleCh[r] == nil {
				r = "miso"
			}
			if b.hasOpp {
				sampleAt[i] = sc.st.levelAt(r, b.opposite.t)
			} else {
				sampleAt[i] = b.mosi
			}
		} else {
			sampleAt[i] = b.mosi
			if proto == "sync" {
				sampleAt[i] = b.mosi
			}
		}
	}
	for off := 0; off+8 <= n; off += 8 {
		val := 0
		clockEdges := []string{}
		boundary := false
		for bit := 0; bit < 8; bit++ {
			if sampleAt[off+bit] == 1 {
				if msbFirst {
					val |= 1 << uint(7-bit)
				} else {
					val |= 1 << uint(bit)
				}
			}
			b := fr.bits[off+bit]
			if b.hasActive {
				clockEdges = append(clockEdges, b.activeEdge.edge.ID)
			}
			if b.hasOpp {
				clockEdges = append(clockEdges, b.opposite.edge.ID)
			}
			if b.boundary {
				boundary = true
			}
		}
		b0 := fr.bits[off]
		b7 := fr.bits[off+7]
		idSrc := fmt.Sprintf("%s|%s|%s|%d|%d|%s|%02x", sc.st.cap.ID, proto, fr.csRole, fi, off/8, m.name, val)
		valid := true
		invalid := ""
		if boundary {
			valid = false
			invalid = "boundary_sample"
		}
		out = append(out, Byte{
			ID: hashNS("by_", idSrc), Index: off / 8, Value: val,
			Hex: fmt.Sprintf("%02X", val), Kind: "data",
			StartNS: edgeTimeOr(b0, true), EndNS: edgeTimeOr(b7, false),
			ClockEdges: clockEdges, FrameEdges: []string{}, Valid: valid, InvalidCode: invalid,
		})
	}
	if rem := n % 8; rem != 0 {
		off := n - rem
		val := 0
		clockEdges := []string{}
		for bit := 0; bit < rem; bit++ {
			if sampleAt[off+bit] == 1 {
				val |= 1 << uint(rem-1-bit)
			}
			b := fr.bits[off+bit]
			if b.hasActive {
				clockEdges = append(clockEdges, b.activeEdge.edge.ID)
			}
		}
		idSrc := fmt.Sprintf("%s|%s|%s|%d|%d|%s|partial", sc.st.cap.ID, proto, fr.csRole, fi, off/8, m.name)
		out = append(out, Byte{
			ID: hashNS("by_", idSrc), Index: off / 8, Value: val,
			Hex: fmt.Sprintf("%02X?", val), Kind: "data",
			StartNS: edgeTimeOr(fr.bits[off], true), EndNS: fr.end,
			ClockEdges: clockEdges, Valid: false, InvalidCode: "missing_clock_edge",
		})
	}
	return out
}

func edgeTimeOr(b spiBit, rising bool) int64 {
	if rising && b.hasActive {
		return b.activeEdge.t
	}
	if !rising && b.hasOpp {
		return b.opposite.t
	}
	if b.hasActive {
		return b.activeEdge.t
	}
	return 0
}

func (sc *spiScanner) annotateSync(fr *spiFrame, cands []Candidate) {
	word := sc.st.cap.Params.SyncWord
	bits := sc.st.cap.Params.SyncBits
	if bits <= 0 {
		bits = 8
	}
	aligned := false
	for ci := range cands {
		c := &cands[ci]
		for bi := range c.Bytes {
			b := &c.Bytes[bi]
			mask := 0xff
			if bits < 8 {
				mask = (1 << uint(bits)) - 1
			}
			if b.Value&mask == word&mask {
				b.Kind = "syncword"
				aligned = true
			}
		}
	}
	if !aligned {
		sc.addFindingOnce("sync.no_sync", "warning", fr.start, fr.end, "data",
			fmt.Sprintf("no candidate aligned to configured sync word 0x%X (%d bits)", word, bits))
	} else {
		sc.addFindingOnce("sync.word", "info", fr.start, fr.end, "data",
			fmt.Sprintf("sync word 0x%X aligned on at least one candidate", word))
	}
}

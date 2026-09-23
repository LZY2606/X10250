package decode

import (
	"sort"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

const spiDecoderVersion = "spi-decoder-1.0.0"

// spiMode captures CPOL/CPHA for one alternative decode.
type spiMode struct {
	name  string
	cpol  int
	cpha  int
}

func allSPIModes() []spiMode {
	return []spiMode{
		{"0", 0, 0}, {"1", 0, 1}, {"2", 1, 0}, {"3", 1, 1},
	}
}

// sampleEdgeKind returns which clock transition is the data capture edge and
// which is the shift edge for CPHA conventions.
func (m spiMode) captureRising() bool {
	if m.cpha == 0 {
		return m.cpol == 1 // leading edge: idle->active; capture when CPOL=1 is falling? see below
	}
	return m.cpol == 0 // trailing edge
}

// captureOnRising returns true when bits are sampled on a logical rising edge.
// CPHA=0: leading edge samples. Idle=0 leading is rising; idle=1 leading falling.
// CPHA=1: trailing edge samples. Idle=0 trailing falling; idle=1 trailing rising.
func captureOnRising(m spiMode) bool {
	if m.cpha == 0 {
		return m.cpol == 0
	}
	return m.cpol == 1
}

// DecodeSPI decodes one capture under SPI parameters. Mode "unknown" yields
// up to four tied candidate transactions per chip-select frame.
func DecodeSPI(c *model.Capture, cfg model.SPIConfig) *model.DecodeResult {
	res := &model.DecodeResult{Bus: "spi", Status: model.StatusOK}
	if cfg.SCLK == "" {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.spi.sclk", Code: "missing_clock", Message: "SPI requires sclk alias"}}
		return res
	}
	tl := buildTimeline(c)
	devByAlias := map[string]model.SPIDevice{}
	aliases := []string{cfg.SCLK, cfg.MOSI, cfg.MISO}
	for _, d := range cfg.Devices {
		devByAlias[d.Alias] = d
		aliases = append(aliases, d.Alias)
	}
	missing := []string{}
	for _, a := range aliases {
		if a != "" && !tl.hasChannel(a) {
			missing = append(missing, a)
		}
	}
	if len(missing) > 0 {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.spi", Code: "unknown_alias", Message: "unknown channel alias(es): " + strings.Join(missing, ",")}}
		return res
	}
	if cfg.WordBits <= 0 {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.spi.word_bits", Code: "missing_word_bits", Message: "word_bits must be positive"}}
		return res
	}

	modes := allSPIModes()
	if cfg.Mode != "unknown" && cfg.Mode != "" {
		picked := pickMode(cfg.Mode)
		if picked == nil {
			res.Status = "semantic_no_solution"
			res.Diagnostics = []model.Diagnostic{{Path: "params.spi.mode", Code: "bad_mode", Message: "mode must be unknown|0|1|2|3", Value: cfg.Mode}}
			return res
		}
		modes = []spiMode{*picked}
	}
	var events []model.DecodeEvent
	var allTrx []*model.DecodedTransaction
	emit := func(ev model.DecodeEvent) { events = append(events, ev) }

	for _, dev := range cfg.Devices {
		frames := csFrames(tl, dev)
		if len(cfg.Devices) > 1 {
			emit(model.DecodeEvent{Code: "spi_multi_device_cs", Severity: "info",
				Message: "multiple chip-select devices decoded independently: " + strconv.Itoa(len(cfg.Devices)) + " devices",
				Data: map[string]any{"device": dev.DeviceID, "frames": len(frames)}})
		}
		for fi, fr := range frames {
			for _, mode := range modes {
				trx, evs := decodeSPIFrame(tl, cfg, dev, mode, fr, fi)
				events = append(events, evs...)
				if trx != nil {
					allTrx = append(allTrx, trx)
				}
			}
		}
	}
	if len(cfg.Devices) == 0 {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.spi.devices", Code: "missing_devices", Message: "SPI requires at least one chip-select device"}}
		return res
	}
	if cfg.Mode == "unknown" || cfg.Mode == "" {
		events = append(events, model.DecodeEvent{Code: "spi_mode_unknown", Severity: "warning",
			Message: "SPI mode unspecified: modes 0..3 retained as tied candidates"})
	}
	res.Transactions = sortTransactions(allTrx)
	res.Events = events
	return res
}

func pickMode(s string) *spiMode {
	for _, m := range allSPIModes() {
		if m.name == s {
			return &m
		}
	}
	return nil
}

type csFrameInfo struct {
	activeEdges []model.Edge
	start       model.Edge
	end         model.Edge
	gaps        [2]int
}

func csActive(dev model.SPIDevice, level int) bool {
	if dev.ActiveHigh {
		return level == 1
	}
	return level == 0
}

func csFrames(tl *timeline, dev model.SPIDevice) []csFrameInfo {
	edges := tl.channelEdges(dev.Alias)
	var out []csFrameInfo
	var cur *csFrameInfo
	for _, e := range edges {
		lv, ok := tl.levelAfter(dev.Alias, e.Picoseconds, e.Seq)
		if !ok {
			continue
		}
		if csActive(dev, lv) && cur == nil {
			cur = &csFrameInfo{start: e, activeEdges: []model.Edge{e}}
			continue
		}
		if !csActive(dev, lv) && cur != nil {
			cur.end = e
			cur.activeEdges = append(cur.activeEdges, e)
			out = append(out, *cur)
			cur = nil
		}
	}
	if cur != nil {
		cur.end = edges[len(edges)-1]
		out = append(out, *cur)
	}
	return out
}

func decodeSPIFrame(tl *timeline, cfg model.SPIConfig, dev model.SPIDevice, mode spiMode, fr csFrameInfo, fi int) (*model.DecodedTransaction, []model.DecodeEvent) {
	var events []model.DecodeEvent
	fromPs, toPs := fr.start.Picoseconds, fr.end.Picoseconds
	clkAll := tl.channelEdges(cfg.SCLK)
	var clock []model.Edge
	for _, e := range clkAll {
		if e.Picoseconds >= fromPs && e.Picoseconds <= toPs {
			clock = append(clock, e)
		}
	}
	captureRise := captureOnRising(mode)
	type sample struct {
		clk      model.Edge
		line     string
		known    bool
		value    int
		dataEdge int
		boundary bool
	}
	take := func(clk model.Edge, line string) sample {
		before, bseq, known := tl.levelAt(line, clk.Picoseconds, clk.Seq)
		after, _ := tl.levelAfter(line, clk.Picoseconds, clk.Seq)
		_ = after
		boundary := false
		for _, a := range append([]string{}, cfg.MOSI, cfg.MISO, dev.Alias) {
			if a == "" {
				continue
			}
			for _, oe := range tl.channelEdges(a) {
				if oe.Picoseconds == clk.Picoseconds && oe.Seq != clk.Seq {
					boundary = true
				}
			}
		}
		if boundary && cfg.BoundarySampling {
			events = append(events, model.DecodeEvent{Code: "boundary_sampling", AtPs: &clk.Picoseconds,
				EdgeSeqs: []int{clk.Seq}, Severity: "warning",
				Message: "SPI data edge coincides with capture clock edge; value retained ambiguously"})
		}
		if !known {
			return sample{clk: clk, line: line, dataEdge: -1, boundary: boundary}
		}
		return sample{clk: clk, line: line, known: true, value: before, dataEdge: bseq, boundary: boundary}
	}

	var mosiBits, misoBits []sample
	gapSplit := false
	for _, e := range clock {
		after, _ := tl.levelAfter(cfg.SCLK, e.Picoseconds, e.Seq)
		isRise := after == 1
		if isRise != captureRise {
			continue
		}
		if g := tl.gapAt(e.Picoseconds); g != nil {
			gapSplit = true
			events = append(events, model.DecodeEvent{Code: "data_gap", AtPs: &e.Picoseconds, EdgeSeqs: []int{e.Seq},
				Severity: "error", Message: "capture edge lies in a data gap; bytes on either side are never stitched",
				Data: map[string]any{"from_ps": g.FromPs, "to_ps": g.ToPs, "device": dev.DeviceID}})
			continue
		}
		if cfg.MOSI != "" {
			mosiBits = append(mosiBits, take(e, cfg.MOSI))
		}
		if cfg.MISO != "" {
			misoBits = append(misoBits, take(e, cfg.MISO))
		}
	}

	key := "spi@" + dev.DeviceID + "#" + strconv.FormatInt(fromPs, 10) + "#" + strconv.Itoa(fi)
	groupUID := GroupUID("spi", key)
	tuid := TrxUID("spi", key+"#mode"+mode.name)
	order := cfg.BitOrder
	if order == "" {
		order = "msb"
	}

	buildBytes := func(line string, bits []sample) []*model.DecodedByte {
		var out []*model.DecodedByte
		wb := cfg.WordBits
		for start := 0; start < len(bits); start += wb {
			end := start + wb
			partial := false
			if end > len(bits) {
				end = len(bits)
				partial = true
			}
			chunk := bits[start:end]
			sb := make([]sampledBit, len(chunk))
			unknown := false
			for i, sm := range chunk {
				var d *int
				if sm.known {
					v := sm.value
					d = &v
				} else {
					unknown = true
				}
				de := sm.dataEdge
				sb[i] = sampledBit{clkEdge: sm.clk, data: d, dataEdge: de, boundary: sm.boundary}
			}
			cands := orderedCandidates(line == "MISO" || line == cfg.MISO, groupUID, line, start/wb, sb, order)
			note := ""
			if partial {
				note = strconv.Itoa(len(chunk)) + " trailing bit(s): CS deasserted mid-word"
				events = append(events, model.DecodeEvent{Code: "spi_truncated_word", AtPs: &chunk[len(chunk)-1].clk.Picoseconds,
					EdgeSeqs: []int{chunk[len(chunk)-1].clk.Seq}, Severity: "error",
					Message: "CS ended mid-word; partial word retained with candidates"})
			}
			if unknown {
				note = "data line level unknown at one or more capture edges"
			}
			out = append(out, &model.DecodedByte{
				UID: ByteUID("spi", key+"#mode"+mode.name, line, start/wb),
				Index: start / wb, Line: line, Kind: "data", Candidates: cands,
				StartEdge: chunk[0].clk.Seq, EndEdge: chunk[len(chunk)-1].clk.Seq, Note: note,
			})
		}
		return out
	}

	var bytes []*model.DecodedByte
	if cfg.MOSI != "" {
		bytes = append(bytes, buildBytes("MOSI", mosiBits)...)
	}
	if cfg.MISO != "" {
		bytes = append(bytes, buildBytes("MISO", misoBits)...)
	}

	state := "complete"
	flags := []string{}
	if gapSplit {
		state = "truncated"
		flags = append(flags, "data_gap")
	}
	if len(clock) == 0 {
		state = "open"
		flags = append(flags, "missing_clock_edge")
		events = append(events, model.DecodeEvent{Code: "i2c_missing_clock_edge", Severity: "error",
			Message: "SPI frame has no active clock edges in CS window (" + dev.DeviceID + ")"})
	}
	for _, b := range bytes {
		if len(b.Candidates) > 1 {
			flags = append(flags, "tied_candidates")
		}
	}
	conf := 1.0
	assume := ""
	if cfg.Mode == "unknown" || cfg.Mode == "" {
		conf = 0.25
		assume = "CPOL=" + strconv.Itoa(mode.cpol) + " CPHA=" + strconv.Itoa(mode.cpha) + " (mode " + mode.name + ")"
	}
	// Tied candidates must compare equal for equivalence; round confidence.
	if conf != 1 {
		conf = 0.25
	}
	trx := &model.DecodedTransaction{
		UID: tuid, GroupUID: groupUID, Bus: "spi", Device: dev.DeviceID,
		Assumption: assume, Confidence: conf,
		StartEdge: fr.start.Seq, EndEdge: fr.end.Seq, State: state, Flags: uniqSorted(flags),
		Bytes: sortBytes(bytes),
	}
	return trx, events
}

func orderedCandidates(isMISO bool, groupUID, line string, idx int, bits []sampledBit, order string) []model.ByteCandidate {
	_ = isMISO
	_ = groupUID
	// Reuse the bit expansion then reverse ordering is encoded directly here.
	unknown := []int{}
	for i, b := range bits {
		if b.data == nil {
			unknown = append(unknown, i)
		}
	}
	combos := 1
	for range unknown {
		combos *= 2
	}
	out := make([]model.ByteCandidate, 0, combos)
	conf := 1.0 / float64(combos)
	for c := 0; c < combos; c++ {
		vals := make([]int, len(bits))
		for i, b := range bits {
			if b.data != nil {
				vals[i] = *b.data
			} else {
				pos := positionOf(unknown, i)
				vals[i] = (c >> (len(unknown) - 1 - pos)) & 1
			}
		}
		val := 0
		for i, v := range vals {
			shift := len(vals) - 1 - i
			if order == "lsb" {
				shift = i
			}
			val |= v << shift
		}
		bs := make([]model.BitSample, len(bits))
		for i, b := range bits {
			vv := vals[i]
			bs[i] = model.BitSample{Index: i, EdgeSeq: b.clkEdge.Seq, Value: &vv, DataEdge: b.dataEdge, Boundary: b.boundary}
		}
		out = append(out, model.ByteCandidate{Value: val, Confidence: round3(conf), Bits: bs})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

func round3(f float64) float64 {
	if f == 0.25 || f == 0.5 || f == 1 {
		return f
	}
	v := float64(int(f*1000+0.5)) / 1000
	return v
}

func uniqSorted(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func sortBytes(bs []*model.DecodedByte) []*model.DecodedByte {
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].Line != bs[j].Line {
			return bs[i].Line < bs[j].Line
		}
		return bs[i].Index < bs[j].Index
	})
	return bs
}

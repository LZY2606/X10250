package decode

import (
	"sort"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

const i2cDecoderVersion = "i2c-decoder-1.0.0"

// i2cState is the finite state machine scanning the merged SCL/SDA edge list.
type i2cState struct {
	tl       *timeline
	cfg      model.I2CConfig
	cap      *model.Capture
	stretch  int64
	events   []model.DecodeEvent
	diags    []model.Diagnostic
	groups   []*trxGroup
}

type trxGroup struct {
	key   string
	trxs  []*model.DecodedTransaction
	flags map[string]bool
	first int
	last  int
}

// DecodeI2C runs the I2C bus reassembly.
func DecodeI2C(c *model.Capture, cfg model.I2CConfig, stretchMinPs int64) *model.DecodeResult {
	res := &model.DecodeResult{Bus: "i2c", Status: model.StatusOK}
	if c == nil {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "$", Code: "no_capture", Message: "no capture supplied"}}
		return res
	}
	if cfg.SCL == "" || cfg.SDA == "" {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params", Code: "missing_clock_lines", Message: "I2C requires scl and sda aliases"}}
		return res
	}
	tl := buildTimeline(c)
	if !tl.hasChannel(cfg.SCL) || !tl.hasChannel(cfg.SDA) {
		res.Status = "semantic_no_solution"
		missing := []string{}
		if !tl.hasChannel(cfg.SCL) {
			missing = append(missing, cfg.SCL)
		}
		if !tl.hasChannel(cfg.SDA) {
			missing = append(missing, cfg.SDA)
		}
		res.Diagnostics = []model.Diagnostic{{Path: "params", Code: "unknown_alias", Message: "unknown channel alias(es): " + strings.Join(missing, ",")}}
		return res
	}
	st := &i2cState{tl: tl, cfg: cfg, cap: c, stretch: stretchMinPs}
	st.run()
	res.Events = st.events
	res.Diagnostics = st.diags
	for _, g := range st.groups {
		res.Transactions = append(res.Transactions, g.trxs...)
	}
	res.Transactions = sortTransactions(res.Transactions)
	return res
}

func (s *i2cState) run() {
	merged := mergedEdges(s.tl, s.cfg.SCL, s.cfg.SDA)
	var (
		started   bool
		cur       *i2cFrame
		groups    []*trxGroup
		groupBy   = map[string]*trxGroup{}
		frameIdx  int
		curGroup  *trxGroup
	)
	for _, e := range merged {
		clkBefore, _, clkKnown := s.tl.levelAt(s.cfg.SCL, e.Picoseconds, e.Seq)
		datBefore, _, datKnown := s.tl.levelAt(s.cfg.SDA, e.Picoseconds, e.Seq)
		clkAfter, _ := s.tl.levelAfter(s.cfg.SCL, e.Picoseconds, e.Seq)
		datAfter, _ := s.tl.levelAfter(s.cfg.SDA, e.Picoseconds, e.Seq)
		isClk := e.Channel == s.cfg.SCL
		boundary := s.boundaryPair(e, merged)

		if clkKnown && datKnown && clkBefore == 1 {
			if isClk && datBefore != datAfter && datAfter == 1 {
				if started || s.likelyStartBefore(e, cur) {
					s.stop(cur, curGroup, e)
					cur, curGroup = nil, nil
					started = false
				}
				continue
			}
			if !isClk && datBefore != datAfter && datAfter == 0 {
				if started {
					s.event("i2c_repeated_start", e, "SDA falls while SCL high: repeated START")
					s.finishOpen(cur, curGroup, &e)
					cur, curGroup = nil, nil
					started = false
				} else {
					s.event("i2c_start", e, "SDA falls while SCL high: START")
				}
				started = true
				cur, curGroup = s.newFrame(frameIdx, e, groupBy, &groups)
				frameIdx++
				continue
			}
		}
		if started && isClk && clkBefore != clkAfter {
			active := clkAfter == 1 // sample on logical rising edge by default
			if s.cfg.SampleEdge == "falling" {
				active = clkAfter == 0
			}
			if active {
				s.sample(cur, curGroup, e, datAfter, datKnown, boundary)
			} else {
				lowStart := e.Picoseconds
				nextClk := s.nextClockEdge(merged, indexOf(merged, e))
				if nextClk != nil {
					lowLen := nextClk.Picoseconds - lowStart
					if s.stretch > 0 && lowLen >= s.stretch {
						s.eventWith("i2c_clock_stretching", e, "SCL low interval "+strconv.FormatInt(lowLen, 10)+"ps meets stretch threshold", map[string]any{"low_ps": lowLen, "threshold_ps": s.stretch})
						if curGroup != nil {
							curGroup.flags["clock_stretching"] = true
						}
					}
				}
			}
		}
	}
	if started {
		s.finishOpen(cur, curGroup, nil)
		s.eventNoEdge("i2c_missing_clock_edge", "capture ends without STOP: clock/control edges missing")
	}
	s.groups = groups
}

// i2cFrame accumulates sampled bits between START and STOP.
type i2cFrame struct {
	idx    int
	start  model.Edge
	bits   []sampledBit
	bytes  []*model.DecodedByte
	open   bool
	state  string
	flags  map[string]bool
}

type sampledBit struct {
	clkEdge model.Edge
	data    *int
	dataEdge int
	boundary bool
}

func (s *i2cState) newFrame(idx int, start model.Edge, by map[string]*trxGroup, groups *[]*trxGroup) (*i2cFrame, *trxGroup) {
	f := &i2cFrame{idx: idx, start: start, open: true, state: "open", flags: map[string]bool{}}
	// Groups with the same start edge coordinate form alternatives of one
	// frame; repeated START starts a new group because it is a real bus event.
	key := "i2c@" + strconv.FormatInt(start.Picoseconds, 10) + "#" + strconv.Itoa(start.Seq)
	g := by[key]
	if g == nil {
		g = &trxGroup{key: key, flags: map[string]bool{}, first: start.Seq, last: start.Seq}
		by[key] = g
		*groups = append(*groups, g)
	}
	return f, g
}

func (s *i2cState) sample(f *i2cFrame, g *trxGroup, clk model.Edge, data int, known bool, boundary bool) {
	if f == nil {
		return
	}
	if gap := s.tl.gapAt(clk.Picoseconds); gap != nil {
		s.eventWith("data_gap", clk, "sample falls inside a data gap; frame split and not stitched", map[string]any{
			"from_ps": gap.FromPs, "to_ps": gap.ToPs,
		})
		s.finishOpen(f, g, &clk)
		return
	}
	var d *int
	if known {
		v := data
		d = &v
	}
	deSeq := -1
	if known {
		_, ds, _ := s.tl.levelAt(s.cfg.SDA, clk.Picoseconds, clk.Seq)
		deSeq = ds
	}
	f.bits = append(f.bits, sampledBit{clkEdge: clk, data: d, dataEdge: deSeq, boundary: boundary})
	if boundary && s.cfg.BoundarySampling {
		s.eventWith("boundary_sampling", clk, "SCL/SDA edges coincide at a sample instant; both legal levels retained as candidates", map[string]any{"policy": "keep_ambiguous"})
		g.flags["boundary_sampling"] = true
	}
	if len(f.bits) == 9 {
		s.commitByte(f, g)
	}
}

func (s *i2cState) commitByte(f *i2cFrame, g *trxGroup) {
	dataBits := f.bits[:8]
	ackBit := f.bits[8]
	bidx := len(f.bytes)
	kind := "data"
	if bidx == 0 {
		kind = "address"
	}
	cands := bitCandidates("i2c", g.key, "SDA", bidx, dataBits)
	startEdge := dataBits[0].clkEdge.Seq
	endEdge := ackBit.clkEdge.Seq
	b := &model.DecodedByte{
		UID:       ByteUID("i2c", g.key, "SDA", bidx),
		Index:     bidx,
		Line:      "SDA",
		Kind:      kind,
		Candidates: cands,
		StartEdge: startEdge,
		EndEdge:   endEdge,
	}
	if len(cands) > 1 {
		b.Note = "multiple legal values: clock edge or data edge missing"
		s.eventWith("i2c_missing_clock_edge", ackBit.clkEdge, "byte "+strconv.Itoa(bidx)+" retains "+strconv.Itoa(len(cands))+" candidates due to missing/ambiguous clock or data edges", map[string]any{"byte_uid": b.UID})
		g.flags["missing_clock_edge"] = true
	}
	f.bytes = append(f.bytes, b)

	ackKind := "ack"
	ackAssume := ""
	if ackBit.data == nil {
		ackKind = "unknown"
		ackAssume = "data level unknown at ACK clock"
		s.eventWith("i2c_missing_clock_edge", ackBit.clkEdge, "ACK bit unknown: data edge missing", map[string]any{"byte_uid": b.UID})
		g.flags["missing_clock_edge"] = true
	} else if *ackBit.data == 1 {
		ackKind = "nack"
	}
	ackCands := bitCandidates("i2c", g.key, "ACK", bidx, []sampledBit{ackBit})
	f.bytes = append(f.bytes, &model.DecodedByte{
		UID:       ByteUID("i2c", g.key, "ACK", bidx),
		Index:     bidx,
		Line:      "SDA",
		Kind:      ackKind,
		Candidates: ackCands,
		StartEdge: ackBit.clkEdge.Seq,
		EndEdge:   ackBit.clkEdge.Seq,
		Note:      ackAssume,
	})
	f.bits = nil
}

func (s *i2cState) stop(f *i2cFrame, g *trxGroup, e model.Edge) {
	s.event("i2c_stop", e, "SDA rises while SCL high: STOP")
	if f == nil {
		return
	}
	if len(f.bits) > 0 {
		s.truncate(f, g)
	}
	f.open = false
	f.state = "complete"
	s.publish(f, g, e.Seq, "", 1.0)
}

func (s *i2cState) finishOpen(f *i2cFrame, g *trxGroup, at *model.Edge) {
	if f == nil {
		return
	}
	lastSeq := -1
	if at != nil {
		lastSeq = at.Seq
	}
	if len(f.bits) > 0 {
		s.truncate(f, g)
	}
	f.open = false
	if f.state == "complete" {
		return
	}
	f.state = "open"
	tr := s.publish(f, g, lastSeq, "no STOP before capture end or next START", 0.5)
	_ = tr
}

func (s *i2cState) truncate(f *i2cFrame, g *trxGroup) {
	n := len(f.bits)
	f.flags["truncated_byte"] = true
	g.flags["truncated_byte"] = true
	partial := make([]sampledBit, n)
	copy(partial, f.bits)
	cands := bitCandidates("i2c", g.key, "SDA", len(f.bytes), partial)
	b := &model.DecodedByte{
		UID: ByteUID("i2c", g.key, "SDA", len(f.bytes)), Index: len(f.bytes),
		Line: "SDA", Kind: "data", Candidates: cands,
		StartEdge: partial[0].clkEdge.Seq, EndEdge: partial[n-1].clkEdge.Seq,
		Note: strconv.Itoa(n) + " trailing bit(s) before frame end",
	}
	f.bytes = append(f.bytes, b)
	f.bits = nil
	s.eventWith("i2c_truncated_byte", partial[n-1].clkEdge, "frame ended mid-byte; partial byte retained with candidates", map[string]any{"bits": n})
}

func (s *i2cState) publish(f *i2cFrame, g *trxGroup, lastSeq int, assume string, conf float64) *model.DecodedTransaction {
	if lastSeq < 0 {
		lastSeq = f.start.Seq
		if len(f.bytes) > 0 {
			lastSeq = f.bytes[len(f.bytes)-1].EndEdge
		}
	}
	tuid := TrxUID("i2c", g.key+"#"+strconv.Itoa(f.idx))
	flags := make([]string, 0, len(f.flags)+len(g.flags))
	for k := range f.flags {
		flags = append(flags, k)
	}
	for k := range g.flags {
		if !f.flags[k] {
			flags = append(flags, k)
		}
	}
	sort.Strings(flags)
	tr := &model.DecodedTransaction{
		UID: tuid, GroupUID: GroupUID("i2c", g.key), Bus: "i2c",
		Assumption: assume, Confidence: conf,
		StartEdge: f.start.Seq, EndEdge: lastSeq, State: f.state,
		Flags: flags, Bytes: f.bytes,
	}
	g.trxs = append(g.trxs, tr)
	if lastSeq > g.last {
		g.last = lastSeq
	}
	return tr
}

func (s *i2cState) likelyStartBefore(e model.Edge, f *i2cFrame) bool { return f != nil }

func (s *i2cState) boundaryPair(e model.Edge, merged []model.Edge) bool {
	i := indexOf(merged, e)
	if i < 0 {
		return false
	}
	if i+1 < len(merged) && merged[i+1].Picoseconds == e.Picoseconds && merged[i+1].Channel != e.Channel {
		return true
	}
	if i > 0 && merged[i-1].Picoseconds == e.Picoseconds && merged[i-1].Channel != e.Channel {
		return true
	}
	return false
}

func (s *i2cState) nextClockEdge(merged []model.Edge, from int) *model.Edge {
	for i := from + 1; i < len(merged); i++ {
		if merged[i].Channel == s.cfg.SCL {
			return &merged[i]
		}
	}
	return nil
}

func (s *i2cState) event(code string, e model.Edge, msg string) {
	s.events = append(s.events, model.DecodeEvent{Code: code, AtPs: &e.Picoseconds, EdgeSeqs: []int{e.Seq}, Severity: severityOf(code), Message: msg})
}

func (s *i2cState) eventWith(code string, e model.Edge, msg string, data map[string]any) {
	s.events = append(s.events, model.DecodeEvent{Code: code, AtPs: &e.Picoseconds, EdgeSeqs: []int{e.Seq}, Severity: severityOf(code), Message: msg, Data: data})
}

func (s *i2cState) eventNoEdge(code, msg string) {
	s.events = append(s.events, model.DecodeEvent{Code: code, Severity: severityOf(code), Message: msg})
}

func severityOf(code string) string {
	switch code {
	case "data_gap", "i2c_missing_clock_edge", "i2c_truncated_byte":
		return "error"
	case "boundary_sampling", "i2c_clock_stretching", "i2c_repeated_start":
		return "warning"
	default:
		return "info"
	}
}

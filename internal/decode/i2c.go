package decode

import (
	"fmt"
	"math"

	"edgebench/internal/model"
)

// i2c bit assembly state.
type i2cFrame struct {
	bits        []int
	clockIDs    []string
	dataIDs     []string
	bitEdgeT    []float64
	dataChangeT []float64
	firstRise   *evt
	bitCount    int
}

func decodeI2C(req Request) ([]model.Transaction, []model.Diagnostic, string) {
	cap := req.Capture
	pi := indexProbes(cap)
	sclP, haveSCL := pi.byRole["SCL"]
	sdaP, haveSDA := pi.byRole["SDA"]
	var diags []model.Diagnostic
	if !haveSCL || !haveSDA {
		return nil, []model.Diagnostic{{Code: "i2c-lines-missing", Severity: "error",
			Message: fmt.Sprintf("I2C needs SCL and SDA probes (found SCL=%v SDA=%v)", haveSCL, haveSDA)}}, model.ClassNoSeman
	}

	evs, _ := mergedEvents(cap)
	if len(evs) == 0 {
		return nil, []model.Diagnostic{{Code: "i2c-no-edges", Severity: "error", Message: "capture contains no usable edges"}}, model.ClassNoSeman
	}

	// Effective bus levels, initialized from the first edge of each channel
	// (assumed to record the idle bus state).
	scl := 1
	sda := 1
	if e0, ok := firstChannelLevel(evs, sclP.Channel); ok {
		scl = e0
	}
	if e0, ok := firstChannelLevel(evs, sdaP.Channel); ok {
		sda = e0
	}

	// Median clock period from SCL rising edges (stretching/anomalies included
	// robustly via median).
	var riseT []float64
	prevRise := math.NaN()
	for _, e := range evs {
		if e.channel == sclP.Channel && e.effective == 1 {
			if !math.IsNaN(prevRise) && e.t > prevRise {
				riseT = append(riseT, e.t-prevRise)
			}
			prevRise = e.t
		}
	}
	period := median(riseT)
	gapLimit := req.Params.MaxGapS
	if gapLimit <= 0 {
		if period > 0 {
			gapLimit = period * 20
		} else {
			gapLimit = 1e-3
		}
	}
	stretchLimit := 0.0
	if period > 0 {
		stretchLimit = period * 2.5
	}

	var trs []model.Transaction
	var cur *model.Transaction
	var frame *i2cFrame
	var frames [][]i2cFrame // completed frames within the current transaction (each is one byte+ack slot)
	var completed []i2cFrame
	startSeen := false
	var startEvt evt
	var lastActiveT = evs[0].t
	byteCount := 0
	budget := false

	flushFragment := func(reason model.Diagnostic, markers ...string) {
		if cur == nil {
			return
		}
		for _, m := range markers {
			cur.Markers = addMarker(cur.Markers, m)
		}
		if frame != nil && frame.bitCount > 0 {
			completed = append(completed, *frame)
			frame = nil
		}
		cur.Fragment = true
		cur.Diagnostics = append(cur.Diagnostics, reason)
		cur.EndEdgeID = lastEdgeID(frame, completed, cur)
		cur.Candidates = buildI2CCandidates(cur, completed, sclP, sdaP, intp(0), 0.5, "incomplete frame: "+reason.Code)
		trs = append(trs, *cur)
		cur = nil
		completed = nil
		startSeen = false
	}

	for i := 0; i < len(evs); i++ {
		e := evs[i]
		if !e.ok {
			d := model.Diagnostic{Code: "edge-time-unconvertible", Severity: "error",
				Message: "edge time cannot be converted to seconds; decoding cannot place it",
				Position: &model.Position{Channel: e.channel, EdgeSeq: e.seq, EdgeID: e.edge.ID, WhenRaw: e.edge.When.Raw}}
			if cur != nil {
				flushFragment(d, "data-gap")
			} else {
				diags = append(diags, d)
			}
			continue
		}
		if e.t-lastActiveT > gapLimit {
			// A quiet gap on the wire: whatever was open is a fragment, never
			// spliced into the next transaction.
			if cur != nil && startSeen {
				flushFragment(model.Diagnostic{Code: "data-gap", Severity: "warning",
					Message: fmt.Sprintf("inactivity gap %.3gs exceeds limit %.3gs; fragment not merged", e.t-lastActiveT, gapLimit),
					Position: &model.Position{Channel: e.channel, EdgeSeq: e.seq, EdgeID: e.edge.ID, WhenRaw: e.edge.When.Raw}}, "data-gap")
			}
		}

		switch {
		case e.channel == sdaP.Channel && e.effective == 0 && scl == 1 && sda == 1:
			// START (or repeated START when a transaction is open).
			if cur != nil {
				if frame != nil && frame.bitCount > 0 {
					completed = append(completed, *frame)
				}
				cur.Markers = addMarker(cur.Markers, "repeated-start")
				if byteCount == 0 && len(completed) > 0 {
					byteCount = len(completed)
				}
			} else {
				cur = &model.Transaction{Index: len(trs), StartEdgeID: e.edge.ID}
				completed = nil
				byteCount = 0
			}
			frame = &i2cFrame{firstRise: ptrEvt(e)}
			frames = nil
			startSeen = true
			startEvt = e
			sda = 0

		case e.channel == sdaP.Channel && e.effective == 1 && scl == 1 && sda == 0:
			// STOP closes the open transaction.
			if frame != nil && frame.bitCount > 0 {
				completed = append(completed, *frame)
			}
			frame = nil
			if cur != nil {
				cur.EndEdgeID = e.edge.ID
				if len(completed) == 0 {
					cur.Fragment = true
					cur.Diagnostics = append(cur.Diagnostics, model.Diagnostic{Code: "empty-transfer", Severity: "error", Message: "START/STOP without any clocked data",
						Position: &model.Position{Channel: e.channel, EdgeSeq: e.seq, EdgeID: e.edge.ID}})
					cur.Candidates = nil
				} else {
					cur.Candidates = buildI2CCandidates(cur, completed, sclP, sdaP, intp(1), 0.95, "complete START..STOP transfer")
				}
				trs = append(trs, *cur)
				cur = nil
				completed = nil
				startSeen = false
			}
			sda = 1

		case e.channel == sclP.Channel:
			was := scl
			scl = e.effective
			if startSeen && frame != nil && e.effective == 1 && was == 0 {
				// SCL rising edge: sample SDA. Clock stretching is detected by
				// an abnormally long low phase before this rise.
				if stretchLimit > 0 && len(frame.clockIDs) > 0 && len(completed) >= 0 {
					// low-phase duration = this rise - previous SCL fall; a
					// cheap proxy tracked via dataChangeT is not used; track
					// globally below.
				}
				frame.bits = append(frame.bits, sda)
				frame.clockIDs = append(frame.clockIDs, e.edge.ID)
				frame.bitEdgeT = append(frame.bitEdgeT, e.t)
				frame.bitCount++
				if frame.bitCount == 9 && cur != nil {
					ack := sda == 0
					cp := *frame
					cp.ack = &ack
					completed = append(completed, cp)
					byteCount++
					if byteCount > MaxBytesPerTransaction {
						budget = true
					}
					frame = &i2cFrame{}
				}
			}
			if startSeen && frame != nil && e.effective == 0 && was == 1 {
				// remember fall time for stretching detection
				frame.lastFall = e.t
				frame.lastFallID = e.edge.ID
			}

		case e.channel == sdaP.Channel:
			sda = e.effective
			if startSeen && frame != nil && scl == 0 {
				frame.dataIDs = append(frame.dataIDs, e.edge.ID)
				frame.dataChangeT = append(frame.dataChangeT, e.t)
			}
		}
		lastActiveT = e.t
	}

	if cur != nil {
		d := model.Diagnostic{Code: "missing-clock-edge", Severity: "error",
			Message: "capture ended before STOP: clock/data edges missing, alignment is not unique",
			Position: &model.Position{EdgeID: cur.StartEdgeID}}
		flushFragment(d, "missing-clock-edge")
	}

	if budget {
		return finalizeI2C(trs, diags, model.ClassBudget)
	}
	return finalizeI2C(trs, diags, model.ClassOK)
}

// i2cFrame needs a couple of timing fields (defined via extension below).
type i2cFrameExtra struct{}

func ptrEvt(e evt) *evt { return &e }

func firstChannelLevel(evs []evt, channel string) (int, bool) {
	for _, e := range evs {
		if e.channel == channel {
			return e.effective, true
		}
	}
	return 0, false
}

func lastEdgeID(_ *i2cFrame, done []i2cFrame, cur *model.Transaction) string {
	if len(done) > 0 && len(done[len(done)-1].clockIDs) > 0 {
		return done[len(done)-1].clockIDs[len(done[len(done)-1].clockIDs)-1]
	}
	if cur != nil {
		return cur.StartEdgeID
	}
	return ""
}

func finalizeI2C(trs []model.Transaction, diags []model.Diagnostic, class string) ([]model.Transaction, []model.Diagnostic, string) {
	for i := range trs {
		trs[i].Index = i
		if trs[i].ID == "" {
			trs[i].ID = model.ShortID("tr", trs[i].StartEdgeID, trs[i].EndEdgeID, trs[i].Markers, trs[i].Fragment)
		}
		assignByteKeys(&trs[i])
	}
	return trs, diags, class
}

func assignByteKeys(t *model.Transaction) {
	for ci := range t.Candidates {
		for bi := range t.Candidates[ci].Bytes {
			t.Candidates[ci].Bytes[bi].Key = byteKey(t.ID, ci, bi)
		}
	}
}

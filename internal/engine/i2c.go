package engine

import (
	"edge-decode-bench/internal/domain"
)

type busResult struct {
	transactions []domain.Transaction
	bytes        []domain.Byte
	candidates   []domain.Candidate
	diagnostics  []domain.Diagnostic
	evidence     []domain.Evidence
}

type frame struct {
	firstStart    string
	startNS       int64
	dataBits      []domain.RawLevel
	boundaryAt    []bool
	dataEdges     []string
	sampleEdges   []string
	ackBit        *domain.RawLevel
	ackEdge       string
	incomplete    bool
	repeatedStart []int64
	stretches     []int64
	gap           bool
	status        string
}

func decodeI2C(c domain.Capture, b domain.Bus, states *signalStates, opts Options, params map[string]any, budget *int) busResult {
	br := busResult{}
	edges := busEdges(c, []string{b.ClockProbe, b.DataProbe}, budget)
	if edges == nil {
		br.diagnostics = append(br.diagnostics, diag("resource_budget_exhausted", "error", b.ID, "budget exhausted before indexing I2C edges", -1, -1))
		return br
	}
	gapThreshold := intParam(params, "data_gap_ns", DefaultDataGapNS)
	stretchThreshold := intParam(params, "clock_stretch_timeout_ns", DefaultStretchTimeoutNS)
	active := domain.RawLevel(b.ClockPolarity)
	idleClock := 1 - active
	var frames []frame
	var cur *frame
	lastRelevant := int64(-1)
	clockLowStart := int64(-1)
	started := false
	scl := states.initial[b.ClockProbe]
	sda := states.initial[b.DataProbe]
	var lastClock, lastData string
	var prevSCL, prevSDA string
	for _, e := range edges {
		*budget--
		if *budget <= 0 {
			br.diagnostics = append(br.diagnostics, diag("resource_budget_exhausted", "error", b.ID, "resource budget exhausted while decoding I2C", -1, -1))
			return br
		}
		prevSCL, prevSDA = lastClock, lastData
		if e.ProbeID == b.DataProbe {
			lastData = e.ID
		}
		if e.ProbeID == b.ClockProbe {
			lastClock = e.ID
		}
		oldSCL, oldSDA := scl, sda
		if e.ProbeID == b.ClockProbe {
			scl = e.Level
		}
		if e.ProbeID == b.DataProbe {
			sda = e.Level
		}
		if lastRelevant >= 0 && e.TimeNS-lastRelevant > gapThreshold {
			if started && cur != nil && len(cur.dataBits) > 0 {
				cur.gap = true
				cur.incomplete = true
				cur.status = "data_gap"
				addEvidence(&br, "data_gap", "warning", b.ID, "gap_between_bit_samples", "data gap exceeds configured threshold; bytes are not stitched into a transaction", lastRelevant, prevSCL, prevSDA)
				frames = append(frames, *cur)
				cur = nil
				started = false
			}
		}
		lastRelevant = e.TimeNS
		if e.ProbeID == b.DataProbe && scl == idleClock && oldSDA != sda {
			if sda == active {
				addEvidence(&br, "i2c_stop", "info", b.ID, "sda_rises_while_scl_idle_high", "I2C STOP observed", e.TimeNS, e.ID, lastClock)
				if cur != nil {
					cur.incomplete = len(cur.dataBits) != 8
					if len(cur.dataBits) == 8 {
						cur.status = "complete"
					}
					frames = append(frames, *cur)
					cur = nil
				}
				started = false
				clockLowStart = -1
				continue
			}
			addEvidence(&br, "i2c_start", "info", b.ID, "sda_falls_while_scl_idle_high", "I2C START observed", e.TimeNS, e.ID, lastClock)
			if cur != nil && started {
				cur.repeatedStart = append(cur.repeatedStart, e.TimeNS)
				addEvidence(&br, "repeated_start", "warning", b.ID, "start_without_stop", "repeated START can continue or begin a parallel legal transaction", e.TimeNS, e.ID, lastClock)
			} else {
				cur = &frame{firstStart: e.ID, startNS: e.TimeNS, status: "complete"}
			}
			started = true
			clockLowStart = -1
			continue
		}
		if e.ProbeID == b.ClockProbe {
			if scl == 1-idleClock && clockLowStart < 0 {
				clockLowStart = e.TimeNS
			}
			if scl == idleClock && clockLowStart >= 0 {
				lowFor := e.TimeNS - clockLowStart
				if active == 1 && lowFor > stretchThreshold {
					if cur != nil {
						cur.stretches = append(cur.stretches, clockLowStart)
					}
					addEvidence(&br, "clock_stretching", "warning", b.ID, "scl_held_active_low", "clock low interval exceeds stretch timeout", clockLowStart, prevSCL, e.ID)
				}
				clockLowStart = -1
			}
			if oldSCL == 1-idleClock && scl == active && started && cur != nil {
				before, _, boundary := levelAt(states, b.DataProbe, e.TimeNS, "before_settle")
				after, _, _ := levelAt(states, b.DataProbe, e.TimeNS, "after_settle")
				bitCount := len(cur.dataBits)
				if bitCount < 8 {
					cur.dataBits = append(cur.dataBits, after)
					cur.boundaryAt = append(cur.boundaryAt, boundary)
					cur.dataEdges = append(cur.dataEdges, lastData)
					cur.sampleEdges = append(cur.sampleEdges, e.ID)
					if boundary && before != after {
						addEvidence(&br, "boundary_sample", "warning", b.ID, "data_transition_at_sample_clock", "SDA changes at the sample boundary", e.TimeNS, e.ID, lastData)
					}
				} else {
					ack := after
					cur.ackBit = &ack
					cur.ackEdge = e.ID
					addEvidence(&br, "i2c_ack_sampled", "info", b.ID, "ninth_clock_data_level", "ACK/NACK level sampled on ninth clock", e.TimeNS, e.ID, lastData)
					frames = append(frames, *cur)
					cur = &frame{firstStart: cur.firstStart, startNS: cur.startNS, status: "complete"}
				}
			}
		}
	}
	if cur != nil && (len(cur.dataBits) > 0 || len(cur.repeatedStart) > 0) {
		cur.incomplete = true
		if cur.status == "complete" {
			cur.status = "missing_clock_edge"
		}
		addEvidence(&br, "missing_clock_edge", "warning", b.ID, "frame_ends_without_full_clock_count", "frame lacks enough clock edges to uniquely finish", -1, cur.firstStart)
		frames = append(frames, *cur)
	}
	for idx, f := range frames {
		txStatus := "complete"
		if f.gap {
			txStatus = "data_gap"
		}
		if f.incomplete {
			txStatus = "incomplete"
		}
		txStart := f.firstStart
		if txStart == "" && len(f.sampleEdges) > 0 {
			txStart = f.sampleEdges[0]
		}
		endEdge := f.ackEdge
		if endEdge == "" {
			if len(f.sampleEdges) > 0 {
				endEdge = f.sampleEdges[len(f.sampleEdges)-1]
			} else {
				endEdge = txStart
			}
		}
		txID := transactionID(opts.RunID, b.ID, txStart, "", idx)
		tx := domain.Transaction{ID: txID, BusID: b.ID, Protocol: "i2c", Label: b.Alias, Status: txStatus, StartEdgeID: txStart, EndEdgeID: endEdge, StartNS: f.startNS, EndNS: edgesEnd(c, endEdge)}
		tx.Evidence = append(tx.Evidence, "i2c_start")
		if len(f.repeatedStart) > 0 {
			tx.Evidence = append(tx.Evidence, "repeated_start")
		}
		if len(f.stretches) > 0 {
			tx.Evidence = append(tx.Evidence, "clock_stretching")
		}
		if f.gap {
			tx.Evidence = append(tx.Evidence, "data_gap")
		}
		if f.incomplete {
			tx.Evidence = append(tx.Evidence, "missing_clock_edge")
		}
		br.transactions = append(br.transactions, tx)
		bid := byteID(opts.RunID, b.ID, txStart, 0)
		var value *int
		if len(f.dataBits) == 8 {
			value = bytesFromBits(f.dataBits)
		}
		confidence := 0.99
		ambiguous := false
		if f.incomplete {
			confidence = 0.35
			ambiguous = true
		}
		role := "data"
		if idx == 0 {
			role = "address_rw"
		}
		ack := ""
		if f.ackBit != nil {
			if *f.ackBit == 0 {
				ack = "ACK"
			} else {
				ack = "NACK"
			}
		}
		bm := domain.Byte{ID: bid, TransactionID: txID, BusID: b.ID, Index: 0, Value: value, Role: role, AckNack: ack, Confidence: confidence,
			StartEdgeID: txStart, SampleEdgeID: firstOr(f.sampleEdges, txStart), EndEdgeID: endEdge, Ambiguous: ambiguous}
		br.bytes = append(br.bytes, bm)
		group := domain.StableID(opts.RunID, b.ID, "byte", bid)
		if value != nil {
			candidate(&br, opts.RunID, b.ID, "byte", bid, group, "after_settle", fmtHex(value), value, confidence, true, "sample_after_data_settle", txStart, endEdge, "i2c_start")
			boundary := false
			for _, x := range f.boundaryAt {
				if x {
					boundary = true
				}
			}
			if boundary {
				alt := alternateBoundary(f.dataBits, f.boundaryAt)
				candidate(&br, opts.RunID, b.ID, "byte", bid, group, "before_settle", fmtHex(&alt), &alt, 0.49, false, "sampled_before_data_settle", txStart, endEdge, "boundary_sample")
			}
		} else {
			candidate(&br, opts.RunID, b.ID, "byte", bid, group, "partial_frame", fmtHex(value), nil, 0.35, true, "missing_clock_edge", txStart, endEdge, "missing_clock_edge")
			candidate(&br, opts.RunID, b.ID, "byte", bid, group, "glitch_no_transaction", "no transaction", nil, 0.25, false, "legal_alternative_when_clock_edges_are_absent", txStart, endEdge, "missing_clock_edge")
		}
		if len(f.repeatedStart) > 0 {
			txgroup := domain.StableID(opts.RunID, b.ID, "transaction", txID)
			candidate(&br, opts.RunID, b.ID, "transaction", txID, txgroup, "continue_on_repeated_start", "one combined transaction", nil, 0.74, true, "i2c_allows_repeated_start_without_stop", txStart, endEdge, "repeated_start")
			candidate(&br, opts.RunID, b.ID, "transaction", txID, txgroup, "split_on_repeated_start", "two sequential transactions", nil, 0.62, false, "repeated_start_may_bound_a_new_read_write_segment", txStart, endEdge, "repeated_start")
		}
		if f.gap {
			gg := domain.StableID(opts.RunID, b.ID, "gap", txID)
			candidate(&br, opts.RunID, b.ID, "transaction", txID, gg, "do_not_stitch", "separated fragment", nil, 0.96, true, "data_gap_forbids_implicit_transaction_join", txStart, endEdge, "data_gap")
			candidate(&br, opts.RunID, b.ID, "transaction", txID, gg, "stitch_anyway", "joined transaction", nil, 0.08, false, "join_lacks_start_and_timing_evidence", txStart, endEdge, "data_gap")
			br.diagnostics = append(br.diagnostics, diag("data_gap", "error", b.ID, "data gap: fragment is not stitched into a transaction", f.startNS, edgesEnd(c, endEdge), txStart, endEdge))
		}
		if f.incomplete && !f.gap {
			br.diagnostics = append(br.diagnostics, diag("missing_clock_edge", "warning", b.ID, "clock edge count is insufficient for a unique byte", f.startNS, edgesEnd(c, endEdge), txStart, endEdge))
		}
		if len(f.stretches) > 0 {
			br.diagnostics = append(br.diagnostics, diag("clock_stretching", "warning", b.ID, "clock stretching observed and retained as timing evidence", f.stretches[0], f.stretches[0], txStart))
		}
	}
	return br
}

func alternateBoundary(bits []domain.RawLevel, boundary []bool) int {
	cp := append([]domain.RawLevel(nil), bits...)
	for i := range cp {
		if i < len(boundary) && boundary[i] {
			cp[i] = 1 - cp[i]
		}
	}
	v := 0
	for _, b := range cp {
		v = (v << 1) | int(b)
	}
	return v
}
func firstOr(xs []string, fallback string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return fallback
}
func edgesEnd(c domain.Capture, id string) int64 {
	for _, e := range c.Edges {
		if e.ID == id {
			return e.TimeNS
		}
	}
	return -1
}

package decode

import (
	"fmt"
	"strconv"

	"edge-decode-station/internal/domain"
)

type i2cEvent struct {
	kind string
	t    float64
	id   string
}

type bitSample struct {
	index      int
	value      *int
	start, end float64
	sample     float64
	edges      []string
	dataEdges  []string
	points     []domain.TimePoint
	missing    bool
}

func decodeI2C(capture domain.Capture, params domain.DecoderParams, signals map[string]*signal, result domain.DecodeResult, budget Budget) (domain.DecodeResult, error) {
	base := basis(capture)
	scl := signals[params.ClockAlias]
	sda := signals[params.DataAlias]
	activeClock := 1
	switch lower := params.ClockPolarityRaw; lower {
	case "active-low", "falling":
		activeClock = 0
	}
	events := buildI2CEvents(scl, sda, activeClock)
	if len(events) == 0 {
		return result, SemanticError{Path: "i2c.events", Message: "no START or clock activity can be decoded"}
	}
	startIndexes := []int{}
	for i, ev := range events {
		if ev.kind == "START" {
			startIndexes = append(startIndexes, i)
		}
	}
	if len(startIndexes) == 0 {
		result.Diagnostics = append(result.Diagnostics, domain.Diagnostic{Code: "semantic_no_solution", Severity: "error", Path: "i2c", Message: "waveform contains no legal START condition", Evidence: nil})
		result.Occurrences = append(result.Occurrences, makeOccurrence("no_start", events[0].t, events[len(events)-1].t, base, "error", "no START condition found", []string{events[0].id, events[len(events)-1].id}, nil, nil))
		return result, nil
	}
	stretch := threshold(params.StretchThreshold, 2000)
	gap := threshold(params.GapThreshold, 5000)
	jitter := threshold(params.JitterWindow, 20)
	var allCandidates []domain.FrameCandidate
	var allOccurrences []domain.Occurrence
	for segNo, startIdx := range startIndexes {
		endIdx := len(events)
		if segNo+1 < len(startIndexes) {
			endIdx = startIndexes[segNo+1]
			allOccurrences = append(allOccurrences, makeOccurrence("repeated_start", events[endIdx].t, events[endIdx].t, base, "info", "repeated START opens a new legal transaction segment", []string{events[endIdx].id}, nil, map[string]any{"previous_start_event_id": events[startIdx].id, "repeated_start_event_id": events[endIdx].id}))
		}
		segmentEvents := events[startIdx:endIdx]
		if len(segmentEvents) < 2 {
			continue
		}
		stopAt := -1
		for i, ev := range segmentEvents {
			if ev.kind == "STOP" {
				stopAt = i
				break
			}
		}
		clockEvents := []i2cEvent{}
		segmentEnd := segmentEvents[len(segmentEvents)-1].t
		hasStop := stopAt >= 0
		if hasStop {
			segmentEnd = segmentEvents[stopAt].t
		}
		for _, ev := range segmentEvents {
			if ev.kind == "EDGE" {
				clockEvents = append(clockEvents, ev)
			}
		}
		samples, occurrences := sampleI2CBits(scl, sda, clockEvents, activeClock, params.SampleBoundary, jitter, stretch, gap, base, params.BusID)
		allOccurrences = append(allOccurrences, occurrences...)
		var groups [][]bitSample
		current := []bitSample{}
		for _, b := range samples {
			if len(current) > 0 && b.start-current[len(current)-1].end > gap {
				allOccurrences = append(allOccurrences, makeOccurrence("data_gap", current[len(current)-1].end, b.start, base, "warning", "data gap is not stitched into one transaction", joinIDs([]string{}, current[len(current)-1].edges, b.edges), nil, map[string]any{"gap_ns": b.start - current[len(current)-1].end, "minimum_ns": gap, "stitch": false}))
				groups = append(groups, current)
				current = []bitSample{}
			}
			current = append(current, b)
		}
		if len(current) > 0 {
			groups = append(groups, current)
		}
		for groupNo, bits := range groups {
			candidate, more := buildI2CCandidate(capture, params, bits, segmentEvents[0], hasStop, stopAt >= 0 && groupNo == len(groups)-1, segNo, groupNo, base, result.Dependencies)
			allCandidates = append(allCandidates, candidate)
			allCandidates = append(allCandidates, more...)
		}
	}
	for i := range allOccurrences {
		allOccurrences[i].Affected = filterAffected(allOccurrences[i].Affected, allCandidates)
	}
	result.Candidates = allCandidates
	result.Occurrences = append(result.Occurrences, allOccurrences...)
	markTied(result.Candidates)
	return result, nil
}

func buildI2CEvents(scl, sda *signal, activeClock int) []i2cEvent {
	out := []i2cEvent{}
	var lastSDA i2cEvent
	haveSDA := false
	for _, p := range sda.transitions() {
		if p.id == "" {
			continue
		}
		t := p.t
		sclLevel := scl.normalizedAt(t)
		if sclLevel == activeClock {
			if haveSDA {
				kind := "START"
				if p.level == 1 && lastSDA.level == 0 {
					kind = "STOP"
				}
				out = append(out, i2cEvent{kind: kind, t: t, id: p.id})
			}
		}
		lastSDA = i2cEvent{t: t, id: p.id, kind: fmt.Sprint(p.level)}
		haveSDA = true
	}
	for _, e := range scl.edges {
		level := scl.norm(e.raw)
		if level == activeClock {
			out = append(out, i2cEvent{kind: "EDGE", t: e.t, id: e.id})
		}
	}
	sortI2CEvents(out)
	return out
}

func sortI2CEvents(events []i2cEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0; j-- {
			a, b := events[j-1], events[j]
			less := a.t < b.t || (a.t == b.t && eventRank(a.kind) < eventRank(b.kind)) || (a.t == b.t && a.kind == b.kind && a.id < b.id)
			if !less {
				break
			}
			events[j-1], events[j] = b, a
		}
	}
}

func eventRank(kind string) int {
	switch kind {
	case "START":
		return 0
	case "EDGE":
		return 1
	case "STOP":
		return 2
	}
	return 3
}

func sampleI2CBits(scl, sda *signal, clocks []i2cEvent, activeClock int, boundary bool, jitter, stretch, gap float64, base, busID string) ([]bitSample, []domain.Occurrence) {
	out := []bitSample{}
	occ := []domain.Occurrence{}
	for i, clock := range clocks {
		start := clock.t
		end := start
		if i+1 < len(clocks) {
			end = clocks[i+1].t
		}
		var sampleT float64
		var sampleIDs []string
		if boundary {
			sampleT = start
			sampleIDs = []string{clock.id}
		} else {
			sampleT = start + (end-start)/2
			sampleIDs = edgeNear(sda, sampleT, jitter)
		}
		if end-start > stretch {
			level := activeClock ^ 1
			occ = append(occ, makeOccurrence("clock_stretching", start, end, base, "info", "clock remains at held level longer than threshold", []string{clock.id}, nil, map[string]any{"duration_ns": end - start, "threshold_ns": stretch, "held_level": level, "continue_transaction": true}))
		}
		value := sda.normalizedAt(sampleT)
		nearby := edgeNear(sda, sampleT, jitter)
		missing := false
		if boundary && len(nearby) > 0 {
			// A transition inside the jitter window of a boundary sample is observable but not silently preferred.
			occ = append(occ, makeOccurrence("boundary_sampling", start, end, base, "warning", "data transition coincides with clock boundary; tied center/boundary candidates retained", append([]string{clock.id}, nearby...), nil, map[string]any{"sample_time_ns": sampleT, "jitter_window_ns": jitter, "alternatives": []string{"boundary", "center"}}))
		}
		b := bitSample{index: i, value: domain.IntPtr(value), start: start, end: end, sample: sampleT, edges: []string{clock.id}, dataEdges: uniqueStrings(append(sampleIDs, nearby...)), points: []domain.TimePoint{domain.TimeAt(sampleT, base)}, missing: missing}
		out = append(out, b)
	}
	mod := len(out) % 9
	if mod != 0 {
		start := out[0].start
		if len(out) > 0 {
			start = out[len(out)-1].end
		}
		occ = append(occ, makeOccurrence("missing_clock_edge", start, clocks[len(clocks)-1].t, base, "warning", "byte frame does not contain nine clock edges; partial legal candidates retained", []string{clocks[len(clocks)-1].id}, nil, map[string]any{"observed_edges": len(out), "required_edges": 9, "missing_edges": 9 - mod, "retain_candidates": true}))
	}
	return out, occ
}

func buildI2CCandidate(capture domain.Capture, params domain.DecoderParams, bits []bitSample, startEvent i2cEvent, hasStartStop, isFinalGroup bool, segNo, groupNo int, base string, deps []domain.DependencyRef) (domain.FrameCandidate, []domain.FrameCandidate) {
	bytes, complete := bitsToBytes(bits, params.BusID, base, capture)
	label := "partial"
	legal := len(bits) > 0
	confidence := 0.25
	reason := "missing clock edge; all legal partial candidates retained"
	if complete && isFinalGroup {
		label = byteLabel(bytes)
		legal = true
		confidence = 0.82
		reason = ""
		if hasStartStop {
			confidence = 0.95
		}
	} else if complete {
		label = byteLabel(bytes)
		confidence = 0.55
		reason = "data gap separates this complete group; it is not concatenated"
	}
	var evidence []string
	for _, b := range bits {
		evidence = append(evidence, b.edges...)
	}
	evidence = append(evidence, startEvent.id)
	candidateID := domain.StableID("i2c-cand-", params.BusID, params.ID, strconv.Itoa(segNo), strconv.Itoa(groupNo), domain.Checksum(evidence))
	start := bits[0].start
	end := bits[len(bits)-1].end
	candidate := domain.FrameCandidate{ID: candidateID, Protocol: "I2C", BusID: params.BusID, Kind: label, ValueLabel: label, Confidence: confidence, Legal: legal, Valid: complete && legal, Reason: reason, Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), Bytes: bytes, Evidence: uniqueStrings(evidence), Fragments: fragmentsFor(capture, start, end), Status: "retained"}
	var extras []domain.FrameCandidate
	if complete {
		boundaryID := domain.StableID("i2c-cand-boundary-", candidateID, "boundary")
		centerID := domain.StableID("i2c-cand-center-", candidateID, "center")
		candidate.ID = boundaryID
		alt := candidate
		alt.ID = centerID
		alt.ModeLabel = "center-sample"
		candidate.ModeLabel = "boundary-sample"
		alt.Confidence = candidate.Confidence - 0.01
		alt.Evidence = append(append([]string{}, candidate.Evidence...), "rule:sample_center")
		extras = append(extras, alt)
		candidate.Evidence = append(candidate.Evidence, "rule:sample_boundary")
	}
	return candidate, extras
}

func bitsToBytes(bits []bitSample, busID, base string, capture domain.Capture) ([]domain.Byte, bool) {
	out := []domain.Byte{}
	complete := true
	for offset := 0; offset < len(bits); offset += 9 {
		chunk := bits[offset:]
		if len(chunk) > 9 {
			chunk = chunk[:9]
		}
		value := 0
		var raw []byte
		var edgeIDs, dataEdges []string
		var points []domain.TimePoint
		start, end := chunk[0].start, chunk[len(chunk)-1].end
		for i := 0; i < 8; i++ {
			if i >= len(chunk) {
				complete = false
				break
			}
			if chunk[i].value != nil {
				value = (value << 1) | *chunk[i].value
			}
			raw = append(raw, byte('0'))
			if chunk[i].value != nil {
				raw[len(raw)-1] = byte('0' + *chunk[i].value)
			}
			edgeIDs = append(edgeIDs, chunk[i].edges...)
			dataEdges = append(dataEdges, chunk[i].dataEdges...)
			points = append(points, chunk[i].points...)
		}
		ack := "?"
		ackValid := false
		var valuePtr *int
		invalidReason := ""
		if len(chunk) >= 8 {
			valuePtr = domain.IntPtr(value)
		} else {
			complete = false
			invalidReason = "missing data clock edge"
		}
		if len(chunk) == 9 {
			ackValid = true
			if chunk[8].value != nil && *chunk[8].value == 0 {
				ack = "ACK"
			} else {
				ack = "NACK"
			}
			edgeIDs = append(edgeIDs, chunk[8].edges...)
			dataEdges = append(dataEdges, chunk[8].dataEdges...)
			points = append(points, chunk[8].points...)
			end = chunk[8].end
		} else {
			complete = false
			invalidReason = "missing ACK clock edge"
		}
		id := domain.StableID("byte-i2c-", busID, string(raw), fmt.Sprint(start), fmt.Sprint(end), ack)
		out = append(out, domain.Byte{ID: id, Index: offset / 9, Value: valuePtr, RawBits: string(raw), Ack: ack, Valid: ackValid && len(chunk) >= 8, InvalidReason: invalidReason, Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), ClockEdgeIDs: uniqueStrings(edgeIDs), DataEdgeIDs: uniqueStrings(dataEdges), SamplePoints: points, FragmentIDs: fragmentsFor(capture, start, end)})
	}
	return out, complete
}

func byteLabel(bytes []domain.Byte) string {
	parts := []string{}
	for _, b := range bytes {
		if b.Value != nil {
			parts = append(parts, fmt.Sprintf("0x%02X", *b.Value))
		} else {
			parts = append(parts, "?")
		}
	}
	return joinLabels(parts)
}

func joinLabels(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " "
		}
		out += p
	}
	return out
}

func threshold(tp domain.TimePoint, fallback float64) float64 {
	if tp.Valid {
		return tp.Nanos
	}
	return fallback
}

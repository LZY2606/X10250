package decode

import (
	"fmt"
	"strconv"

	"edge-decode-station/internal/domain"
)

type spiModeSpec struct {
	mode, cpol, cpha, sampleEdge, shiftEdge int
	label                                   string
}

func parseSPIMode(s string) (spiModeSpec, bool) {
	switch s {
	case "0":
		return spiModeSpec{0, 0, 0, 1, 0, "mode 0 CPOL=0 CPHA=0"}, true
	case "1":
		return spiModeSpec{1, 0, 1, 0, 1, "mode 1 CPOL=0 CPHA=1"}, true
	case "2":
		return spiModeSpec{2, 1, 0, 0, 1, "mode 2 CPOL=1 CPHA=0"}, true
	case "3":
		return spiModeSpec{3, 1, 1, 1, 0, "mode 3 CPOL=1 CPHA=1"}, true
	default:
		return spiModeSpec{}, false
	}
}

func decodeSPI(capture domain.Capture, params domain.DecoderParams, signals map[string]*signal, result domain.DecodeResult, budget Budget) (domain.DecodeResult, error) {
	base := basis(capture)
	sclk := signals[params.ClockAlias]
	mosi := signals[params.DataAlias]
	gap := threshold(params.GapThreshold, 5000)
	jitter := threshold(params.JitterWindow, 20)
	modes := []spiModeSpec{}
	if params.Mode == "" || params.Mode == "unknown" {
		for m := 0; m < 4; m++ {
			spec, _ := parseSPIMode(strconv.Itoa(m))
			modes = append(modes, spec)
		}
		result.Occurrences = append(result.Occurrences, makeOccurrence("spi_mode_unknown", 0, 0, base, "info", "SPI mode is not uniquely specified; legal mode candidates retained", nil, nil, map[string]any{"candidate_modes": []int{0, 1, 2, 3}}))
	} else {
		spec, ok := parseSPIMode(params.Mode)
		if !ok {
			return result, SemanticError{Path: "spi_mode", Message: "unsupported SPI mode"}
		}
		modes = []spiModeSpec{spec}
	}
	var candidates []domain.FrameCandidate
	var occurrences []domain.Occurrence
	for deviceIndex, csAlias := range params.ChipSelectAliases {
		cs := signals[csAlias]
		windows := activeWindows(cs, params.ActiveLowSelect)
		if len(windows) > 1 {
			for i := 1; i < len(windows); i++ {
				occurrences = append(occurrences, makeOccurrence("multi_device_chip_select", windows[i].start, windows[i].end, base, "info", "separate chip-select interval decoded independently", windows[i].ids, nil, map[string]any{"chip_select": csAlias, "device_index": deviceIndex, "window_index": i, "merge": false}))
			}
		}
		for windowIndex, window := range windows {
			clockEdges := clocksInWindow(sclk, window)
			if len(clockEdges) == 0 {
				occurrences = append(occurrences, makeOccurrence("missing_clock_edge", window.start, window.end, base, "warning", "selected SPI device has no clock edge", window.ids, nil, map[string]any{"chip_select": csAlias, "retain_candidates": false}))
				continue
			}
			groups := splitClockGroups(clockEdges, gap)
			for groupIndex, clocks := range groups {
				if groupIndex > 0 {
					prev := groups[groupIndex-1]
					occurrences = append(occurrences, makeOccurrence("data_gap", prev[len(prev)-1].t, clocks[0].t, base, "warning", "SPI data gap under chip select is not concatenated", []string{prev[len(prev)-1].id, clocks[0].id}, nil, map[string]any{"gap_ns": clocks[0].t - prev[len(prev)-1].t, "threshold_ns": gap, "stitch": false, "chip_select": csAlias}))
				}
				modeCandidates := []domain.FrameCandidate{}
				for _, mode := range modes {
					candidate, okay := buildSPICandidate(capture, params, sclk, mosi, csAlias, deviceIndex, windowIndex, groupIndex, clocks, mode, boundary(params), jitter, gap, base)
					if okay {
						modeCandidates = append(modeCandidates, candidate)
					}
				}
				if len(clocks)%8 != 0 {
					occurrences = append(occurrences, makeOccurrence("missing_clock_edge", clocks[0].t, clocks[len(clocks)-1].t, base, "warning", "SPI clock count is not byte aligned; partial candidates retained", []string{clocks[0].id, clocks[len(clocks)-1].id}, nil, map[string]any{"observed_edges": len(clocks), "missing_edges": 8 - len(clocks)%8, "retain_candidates": true, "chip_select": csAlias}))
				}
				if len(modes) == 4 && len(modeCandidates) > 1 {
					for i := range modeCandidates {
						modeCandidates[i].Confidence = 0.55
					}
					occurrences = append(occurrences, makeOccurrence("parallel_candidates", clocks[0].t, clocks[len(clocks)-1].t, base, "info", "multiple legal SPI modes remain tied", []string{clocks[0].id}, nil, map[string]any{"chip_select": csAlias, "candidate_count": len(modeCandidates), "tied": true}))
				}
				candidates = append(candidates, modeCandidates...)
			}
		}
	}
	if len(candidates) == 0 {
		return result, SemanticError{Path: "spi.candidates", Message: "no legal SPI candidate can be formed"}
	}
	for i := range occurrences {
		occurrences[i].Affected = filterAffected(occurrences[i].Affected, candidates)
	}
	result.Candidates = candidates
	result.Occurrences = append(result.Occurrences, occurrences...)
	markTied(result.Candidates)
	return result, nil
}

type timeWindow struct {
	start, end float64
	ids        []string
}

func activeWindows(cs *signal, activeLow bool) []timeWindow {
	active := 1
	if activeLow {
		active = 0
	}
	var windows []timeWindow
	var current *timeWindow
	last := -1.0
	level := cs.norm(cs.initial)
	if level == active {
		current = &timeWindow{start: -1}
	}
	for _, e := range cs.edges {
		level = cs.norm(e.raw)
		if current != nil {
			current.ids = append(current.ids, e.id)
			current.end = e.t
		}
		if level == active {
			if current == nil {
				current = &timeWindow{start: e.t, ids: []string{e.id}}
			}
		} else if current != nil {
			if current.start < 0 {
				current.start = 0
			}
			if current.end == 0 {
				current.end = e.t
			}
			windows = append(windows, *current)
			current = nil
		}
		last = e.t
	}
	if current != nil {
		if current.start < 0 {
			current.start = 0
		}
		if current.end == 0 {
			current.end = last
		}
		windows = append(windows, *current)
	}
	return windows
}

func clocksInWindow(sclk *signal, window timeWindow) []edgeEvent {
	out := []edgeEvent{}
	for _, e := range sclk.edges {
		if e.t >= window.start && e.t <= window.end {
			out = append(out, e)
		}
	}
	return out
}

func splitClockGroups(clocks []edgeEvent, gap float64) [][]edgeEvent {
	var groups [][]edgeEvent
	current := []edgeEvent{}
	var previous float64
	for _, c := range clocks {
		if len(current) > 0 && c.t-previous > gap {
			groups = append(groups, current)
			current = []edgeEvent{}
		}
		current = append(current, c)
		previous = c.t
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func boundary(params domain.DecoderParams) bool { return params.SampleBoundary }

func buildSPICandidate(capture domain.Capture, params domain.DecoderParams, sclk, mosi *signal, csAlias string, deviceIndex, windowIndex, groupIndex int, clocks []edgeEvent, mode spiModeSpec, useBoundary bool, jitter, gap float64, base string) (domain.FrameCandidate, bool) {
	var samples []edgeEvent
	for _, c := range clocks {
		level := sclk.norm(c.raw)
		if level == mode.sampleEdge {
			samples = append(samples, c)
		}
	}
	if len(samples) == 0 {
		return domain.FrameCandidate{}, false
	}
	bytes, complete := spiBitsToBytes(samples, mosi, params.BusID, csAlias, mode.label, base, capture, useBoundary, jitter)
	start := samples[0].t
	end := samples[len(samples)-1].t
	label := byteLabel(bytes)
	confidence := 0.9
	if !complete {
		confidence = 0.35
	}
	idParts := []string{params.BusID, params.ID, csAlias, mode.label, fmt.Sprint(windowIndex), fmt.Sprint(groupIndex), fmt.Sprint(start), fmt.Sprint(end)}
	candidate := domain.FrameCandidate{ID: domain.StableID("spi-cand-", idParts...), Protocol: "SPI", BusID: params.BusID, Device: csAlias, ModeLabel: mode.label, Kind: label, ValueLabel: label, Confidence: confidence, Legal: true, Valid: complete, Reason: mapReason(complete), Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), Bytes: bytes, Evidence: spiEvidence(samples, mosi, csAlias, mode), Fragments: fragmentsFor(capture, start, end), Status: "retained"}
	return candidate, true
}

func mapReason(complete bool) string {
	if complete {
		return ""
	}
	return "missing SPI clock edge; partial legal candidate retained"
}

func spiBitsToBytes(samples []edgeEvent, mosi *signal, busID, csAlias, modeLabel, base string, capture domain.Capture, useBoundary bool, jitter float64) ([]domain.Byte, bool) {
	out := []domain.Byte{}
	complete := true
	for offset := 0; offset < len(samples); offset += 8 {
		chunk := samples[offset:]
		if len(chunk) > 8 {
			chunk = chunk[:8]
		}
		value := 0
		raw := ""
		var clockIDs, dataIDs []string
		var points []domain.TimePoint
		for _, sample := range chunk {
			bit := mosi.normalizedAt(sample.t)
			value = (value << 1) | bit
			raw += fmt.Sprint(bit)
			clockIDs = append(clockIDs, sample.id)
			dataIDs = append(dataIDs, edgeNear(mosi, sample.t, jitter)...)
			points = append(points, domain.TimeAt(sample.t, base))
		}
		valid := len(chunk) == 8
		if !valid {
			complete = false
		}
		var ptr *int
		reason := ""
		if valid {
			ptr = domain.IntPtr(value)
		} else {
			reason = "fewer than eight sample edges"
		}
		start, end := chunk[0].t, chunk[len(chunk)-1].t
		id := domain.StableID("byte-spi-", busID, csAlias, modeLabel, raw, fmt.Sprint(start), fmt.Sprint(end))
		out = append(out, domain.Byte{ID: id, Index: offset / 8, Value: ptr, RawBits: raw, Ack: "N/A", Valid: valid, InvalidReason: reason, Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), ClockEdgeIDs: uniqueStrings(clockIDs), DataEdgeIDs: uniqueStrings(dataIDs), SamplePoints: points, FragmentIDs: fragmentsFor(capture, start, end)})
	}
	return out, complete
}

func spiEvidence(samples []edgeEvent, mosi *signal, csAlias string, mode spiModeSpec) []string {
	ids := []string{"channel:" + csAlias, "rule:" + mode.label}
	for _, s := range samples {
		ids = append(ids, s.id)
	}
	ids = append(ids, edgeNear(mosi, samples[0].t, 0)...)
	return uniqueStrings(ids)
}

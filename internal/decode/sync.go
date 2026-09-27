package decode

import (
	"fmt"

	"edge-decode-station/internal/domain"
)

func decodeSYNC(capture domain.Capture, params domain.DecoderParams, signals map[string]*signal, result domain.DecodeResult, budget Budget) (domain.DecodeResult, error) {
	base := basis(capture)
	clock := signals[params.ClockAlias]
	data := signals[params.DataAlias]
	valid := signals[params.ValidAlias]
	gap := threshold(params.GapThreshold, 5000)
	jitter := threshold(params.JitterWindow, 20)
	sampleLevel := 1
	if params.ClockPolarityRaw == "falling" || params.ClockPolarityRaw == "active-low" {
		sampleLevel = 0
	}
	windows := activeWindows(valid, false)
	var candidates []domain.FrameCandidate
	var occurrences []domain.Occurrence
	for wi, window := range windows {
		clocks := clocksInWindow(clock, window)
		if len(clocks) == 0 {
			occurrences = append(occurrences, makeOccurrence("missing_clock_edge", window.start, window.end, base, "warning", "synchronous valid window contains no active clock edge", window.ids, nil, map[string]any{"valid": params.ValidAlias, "retain_candidates": false}))
			continue
		}
		groups := splitClockGroups(clocks, gap)
		for gi, group := range groups {
			if gi > 0 {
				prev := groups[gi-1]
				occurrences = append(occurrences, makeOccurrence("data_gap", prev[len(prev)-1].t, group[0].t, base, "warning", "synchronous data gap is not concatenated", []string{prev[len(prev)-1].id, group[0].id}, nil, map[string]any{"stitch": false, "gap_ns": group[0].t - prev[len(prev)-1].t, "threshold_ns": gap}))
			}
			samples := []edgeEvent{}
			for _, c := range group {
				if clock.norm(c.raw) == sampleLevel {
					samples = append(samples, c)
				}
			}
			bytes, complete := spiBitsToBytes(samples, data, params.BusID, params.ValidAlias, "custom-sync", base, capture, params.SampleBoundary, jitter)
			start, end := samples[0].t, samples[len(samples)-1].t
			confidence := 0.86
			reason := ""
			if !complete {
				confidence = 0.32
				reason = "incomplete synchronous word retained as partial candidate"
			}
			id := domain.StableID("sync-cand-", params.BusID, params.ID, params.ValidAlias, fmt.Sprint(wi), fmt.Sprint(gi), fmt.Sprint(start), fmt.Sprint(end))
			evidence := append([]string{}, window.ids...)
			for _, s := range samples {
				evidence = append(evidence, s.id)
			}
			candidates = append(candidates, domain.FrameCandidate{ID: id, Protocol: "SYNC", BusID: params.BusID, Device: params.ValidAlias, ModeLabel: params.ClockPolarityRaw, Kind: "custom", ValueLabel: byteLabel(bytes), Confidence: confidence, Legal: true, Valid: complete, Reason: reason, Start: domain.TimeAt(start, base), End: domain.TimeAt(end, base), Bytes: bytes, Evidence: uniqueStrings(evidence), Fragments: fragmentsFor(capture, start, end), Status: "retained"})
		}
	}
	if len(candidates) == 0 {
		return result, SemanticError{Path: "sync.candidates", Message: "no valid synchronous transaction found"}
	}
	for i := range occurrences {
		occurrences[i].Affected = filterAffected(occurrences[i].Affected, candidates)
	}
	result.Candidates = candidates
	result.Occurrences = append(result.Occurrences, occurrences...)
	return result, nil
}

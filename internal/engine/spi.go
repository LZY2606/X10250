package engine

import (
	"edge-decode-bench/internal/domain"
)

type spiSample struct {
	at       int64
	clock    string
	data     string
	boundary bool
	before   domain.RawLevel
	after    domain.RawLevel
}
type spiWindow struct {
	probe      string
	start      int64
	end        int
	startEdge  string
	endEdge    string
	samples    map[string][]spiSample
	dataProbes []string
}

func decodeSPI(c domain.Capture, b domain.Bus, states *signalStates, opts Options, params map[string]any, budget *int, custom bool) busResult {
	br := busResult{}
	dataProbes := []string{}
	if b.DataProbe != "" {
		dataProbes = append(dataProbes, b.DataProbe)
	}
	if b.DataInProbe != "" {
		dataProbes = append(dataProbes, b.DataInProbe)
	}
	edges := busEdges(c, append(append([]string{}, b.SelectProbes...), busProbes(b)...), budget)
	if edges == nil {
		br.diagnostics = append(br.diagnostics, diag("resource_budget_exhausted", "error", b.ID, "budget exhausted before indexing SPI edges", -1, -1))
		return br
	}
	gapThreshold := intParam(params, "data_gap_ns", DefaultDataGapNS)
	windows := buildSPIWindows(b, states, edges, dataProbes, gapThreshold, &br)
	modes := requestedModes(b)
	txn := 0
	for _, w := range windows {
		for _, dataProbe := range w.dataProbes {
			samples := w.samples[dataProbe]
			fragments := splitSamples(samples, gapThreshold, b, &br, w)
			for fragIdx, frag := range fragments {
				start := frag[0]
				end := frag[len(frag)-1]
				txID := transactionID(opts.RunID, b.ID, start.clock, w.probe, txn)
				txn++
				status := "complete"
				ev := []string{"spi_chip_select"}
				if len(frag)%8 != 0 {
					status = "missing_clock_edge"
					ev = append(ev, "missing_clock_edge")
				}
				if fragIdx > 0 || start.at-w.start > gapThreshold {
					status = "data_gap"
					ev = append(ev, "data_gap")
				}
				tx := domain.Transaction{ID: txID, BusID: b.ID, Protocol: b.Protocol, Label: b.Alias, Status: status,
					StartEdgeID: w.startEdge, EndEdgeID: w.endEdge, StartNS: w.start, EndNS: w.end, SelectProbe: w.probe, Evidence: ev}
				br.transactions = append(br.transactions, tx)
				byteCount := len(frag) / 8
				if len(frag)%8 != 0 {
					byteCount++
				}
				if byteCount == 0 {
					continue
				}
				for idx := 0; idx < byteCount; idx++ {
					lo := idx * 8
					hi := lo + 8
					partial := false
					if hi > len(frag) {
						hi = len(frag)
						partial = true
					}
					part := frag[lo:hi]
					bid := domain.StableID(opts.RunID, "byte", b.ID, txID, dataProbe, idx)
					group := domain.StableID(opts.RunID, b.ID, "spi-byte", bid)
					bestVal := (*int)(nil)
					bestConf := 0.0
					ambiguous := len(modes) > 1 || partial
					for mi, mode := range modes {
						bits := make([]domain.RawLevel, 0, 8)
						boundary := false
						for _, s := range part {
							level := s.after
							if b.SampleBoundary == "before_settle" {
								level = s.before
							}
							bits = append(bits, level)
							if s.boundary {
								boundary = true
							}
						}
						var val *int
						if !partial {
							v := 0
							for _, bit := range bits {
								v = (v << 1) | int(bit)
							}
							val = &v
						}
						conf := 0.96/float64(len(modes)) + 0.02
						if partial {
							conf = 0.24
						}
						adoptMode := (mi == 0 && b.ClockPolarity >= 0 && b.CPHA >= 0)
						adopt := adoptMode
						label := modeLabel(mode)
						reason := "mode_matches_cpol_cpha"
						if len(modes) > 1 {
							reason = "spi_mode_not_uniquely_determined"
							adopt = false
							conf = 0.72
						}
						if partial {
							reason = "missing_clock_edge"
							adopt = adoptMode && len(modes) == 1
						}
						if boundary {
							primary := levelChoice(part, b.SampleBoundary)
							if !partial {
								best := bitsToInt(primary)
								val = &best
							}
							altLevel := "before_settle"
							if b.SampleBoundary == "before_settle" {
								altLevel = "after_settle"
							}
							alt := bitsToInt(levelChoice(part, altLevel))
							adoptBoundary := adoptMode && b.SampleBoundary == "after_settle"
							if len(modes) == 1 {
								adopt = adoptBoundary
							}
							candidate(&br, opts.RunID, b.ID, "byte", bid, group, label+" after_settle", fmtHex(&alt), nil, 0.58, b.SampleBoundary == "after_settle" && adoptMode, "boundary_after_settle", start.clock, end.clock, "boundary_sample")
							candidate(&br, opts.RunID, b.ID, "byte", bid, group, label+" before_settle", fmtHex(&alt), &alt, 0.39, b.SampleBoundary == "before_settle" && adoptMode, "boundary_before_settle", start.clock, end.clock, "boundary_sample")
						} else {
							candidate(&br, opts.RunID, b.ID, "byte", bid, group, label, fmtHex(val), val, conf, adopt, reason, start.clock, end.clock, "spi_chip_select")
						}
						if adopt && val != nil && conf > bestConf {
							bestVal = val
							bestConf = conf
						}
					}
					if status == "data_gap" && idx == 0 {
						gg := domain.StableID(opts.RunID, b.ID, "gap", txID, idx)
						candidate(&br, opts.RunID, b.ID, "transaction", txID, gg, "do_not_stitch", "separated SPI fragment", nil, 0.96, true, "data_gap_forbids_implicit_transaction_join", start.clock, end.clock, "data_gap")
						candidate(&br, opts.RunID, b.ID, "transaction", txID, gg, "stitch_anyway", "joined SPI transaction", nil, 0.08, false, "join_lacks_contiguous_clock_evidence", start.clock, end.clock, "data_gap")
					}
					role := "data"
					if custom {
						role = "sync_word"
					}
					bm := domain.Byte{ID: bid, TransactionID: txID, BusID: b.ID, Index: idx, Value: bestVal, Role: role, Confidence: bestConf,
						StartEdgeID: start.clock, SampleEdgeID: start.clock, EndEdgeID: end.clock, Ambiguous: ambiguous}
					br.bytes = append(br.bytes, bm)
					if partial {
						br.diagnostics = append(br.diagnostics, diag("missing_clock_edge", "warning", b.ID, "SPI frame ends before eight sampled bits", start.at, end.at, start.clock, end.clock))
						addEvidence(&br, "missing_clock_edge", "warning", b.ID, "chip_select_window_short", "clock edges are insufficient to uniquely finish SPI byte", start.at, start.clock, end.clock)
					}
				}
			}
			if gapEvidenceNeeded(w, dataProbe, gapThreshold) {
				br.diagnostics = append(br.diagnostics, diag("data_gap", "error", b.ID, "SPI data gap: fragments are kept as separate transactions", w.start, w.end, w.startEdge, w.endEdge))
			}
		}
		if len(windows) > 1 {
			br.diagnostics = append(br.diagnostics, diag("multi_device_chip_select", "info", b.ID, "multiple chip-select windows decoded as separate devices or transactions", w.start, w.end, w.startEdge, w.endEdge))
		}
	}
	return br
}

type spiMode struct{ cpol, cpha int }

func requestedModes(b domain.Bus) []spiMode {
	pols := []int{b.ClockPolarity}
	phas := []int{b.CPHA}
	if b.ClockPolarity < 0 {
		pols = []int{0, 1}
	}
	if b.CPHA < 0 {
		phas = []int{0, 1}
	}
	var out []spiMode
	for _, p := range pols {
		for _, a := range phas {
			out = append(out, spiMode{p, a})
		}
	}
	return out
}
func modeLabel(m spiMode) string {
	switch m {
	case spiMode{0, 0}:
		return "mode 0 / CPOL=0 CPHA=0"
	case spiMode{0, 1}:
		return "mode 1 / CPOL=0 CPHA=1"
	case spiMode{1, 0}:
		return "mode 2 / CPOL=1 CPHA=0"
	case spiMode{1, 1}:
		return "mode 3 / CPOL=1 CPHA=1"
	}
	return "unknown SPI mode"
}
func buildSPIWindows(b domain.Bus, states *signalStates, edges []domain.Edge, dataProbes []string, gap int64, br *busResult) []spiWindow {
	selects := b.SelectProbes
	if len(selects) == 0 {
		selects = []string{""}
	}
	var out []spiWindow
	for _, sp := range selects {
		activeLevel := domain.RawLevel(0)
		if b.SelectPolarity == 1 {
			activeLevel = 1
		}
		var current *spiWindow
		currentLevel := domain.LevelHigh
		if sp != "" {
			currentLevel = states.initial[sp]
		}
		flush := func(endEdge domain.Edge) {
			if current == nil {
				return
			}
			current.end = endEdge.TimeNS
			current.endEdge = endEdge.ID
			out = append(out, *current)
			current = nil
		}
		for _, e := range edges {
			if sp != "" {
				if e.ProbeID != sp {
					if current == nil {
						continue
					}
				} else {
					prev := currentLevel
					currentLevel = e.Level
					if prev != activeLevel && currentLevel == activeLevel {
						current = &spiWindow{probe: sp, start: e.TimeNS, startEdge: e.ID, end: e.TimeNS, endEdge: e.ID, samples: map[string][]spiSample{}, dataProbes: dataProbes}
						addEvidence(br, "spi_chip_select", "info", b.ID, "chip_select_active_level", "chip-select window opened", e.TimeNS, e.ID)
					} else if prev == activeLevel && currentLevel != activeLevel {
						flush(e)
					}
					continue
				}
			} else if current == nil {
				current = &spiWindow{probe: "", start: edges[0].TimeNS, startEdge: edges[0].ID, end: edges[len(edges)-1].TimeNS, endEdge: edges[len(edges)-1].ID, samples: map[string][]spiSample{}, dataProbes: dataProbes}
			}
			if e.ProbeID != b.ClockProbe {
				continue
			}
			modeSampleEdge := isSPISampleEdge(states, b, e.ID, e.TimeNS)
			if !modeSampleEdge {
				continue
			}
			for _, dp := range dataProbes {
				before, _, boundary := levelAt(states, dp, e.TimeNS, "before_settle")
				after, _, _ := levelAt(states, dp, e.TimeNS, "after_settle")
				current.samples[dp] = append(current.samples[dp], spiSample{at: e.TimeNS, clock: e.ID, data: edgeAtProbe(states, dp, e.TimeNS), boundary: boundary, before: before, after: after})
			}
		}
		if current != nil {
			out = append(out, *current)
		}
	}
	return out
}
func isSPISampleEdge(states *signalStates, b domain.Bus, clockEdge string, at int64) bool {
	points := states.byProbe[b.ClockProbe]
	for i, p := range points {
		if p.edgeID != clockEdge {
			continue
		}
		if i == 0 {
			return false
		}
		prev := points[i-1].level
		next := p.level
		if b.ClockPolarity == -1 {
			return prev != next
		}
		idle := domain.RawLevel(b.ClockPolarity)
		if b.CPHA < 0 {
			return prev != next
		}
		if b.CPHA == 0 {
			return prev == idle && next == 1-idle
		}
		return prev == 1-idle && next == idle
	}
	return false
}
func edgeAtProbe(states *signalStates, probe string, at int64) string {
	for _, p := range states.byProbe[probe] {
		if p.at == at {
			return p.edgeID
		}
	}
	return ""
}
func splitSamples(samples []spiSample, gap int64, b domain.Bus, br *busResult, w spiWindow) [][]spiSample {
	if len(samples) == 0 {
		return nil
	}
	var out [][]spiSample
	cur := []spiSample{samples[0]}
	for i := 1; i < len(samples); i++ {
		if samples[i].at-samples[i-1].at > gap {
			addEvidence(br, "data_gap", "warning", b.ID, "gap_between_spi_sample_edges", "sample gap exceeds threshold; SPI bytes are not stitched", samples[i].at, samples[i-1].clock, samples[i].clock)
			out = append(out, cur)
			cur = []spiSample{}
		}
		cur = append(cur, samples[i])
	}
	out = append(out, cur)
	return out
}
func gapEvidenceNeeded(w spiWindow, probe string, gap int64) bool {
	prev := int64(-1)
	for _, s := range w.samples[probe] {
		if prev >= 0 && s.at-prev > gap {
			return true
		}
		prev = s.at
	}
	return false
}
func bitsToInt(bits []domain.RawLevel) int {
	v := 0
	for _, b := range bits {
		v = (v << 1) | int(b)
	}
	return v
}
func levelChoice(part []spiSample, boundary string) []domain.RawLevel {
	out := make([]domain.RawLevel, 0, len(part))
	for _, s := range part {
		if boundary == "before_settle" {
			out = append(out, s.before)
		} else {
			out = append(out, s.after)
		}
	}
	return out
}
func postCustom(br *busResult) {
	for i := range br.transactions {
		br.transactions[i].Protocol = "custom_sync"
		br.transactions[i].Label += " custom sync"
	}
	for i := range br.bytes {
		br.bytes[i].Role = "sync_word"
	}
}

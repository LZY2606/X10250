package decode

import (
	"sort"
	"strings"

	"edgebench/internal/model"
)

const customDecoderVersion = "custom-sync-decoder-1.0.0"

// DecodeCustom decodes a user-defined synchronous line set: one clock and one
// or more data lines sampled on an explicit edge.
func DecodeCustom(c *model.Capture, cfg model.CustomConfig) *model.DecodeResult {
	res := &model.DecodeResult{Bus: "custom", Status: model.StatusOK}
	if cfg.Clock == "" {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.custom.clock", Code: "missing_clock", Message: "custom decoder requires clock alias"}}
		return res
	}
	if cfg.SampleEdge != "rising" && cfg.SampleEdge != "falling" {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.custom.sample_edge", Code: "bad_sample_edge", Message: "sample_edge must be rising or falling", Value: cfg.SampleEdge}}
		return res
	}
	if cfg.WordBits <= 0 {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.custom.word_bits", Code: "missing_word_bits", Message: "word_bits must be positive"}}
		return res
	}
	tl := buildTimeline(c)
	missing := []string{cfg.Clock}
	seen := map[string]bool{}
	for _, l := range cfg.Lines {
		if !seen[l] {
			missing = append(missing, l)
			seen[l] = true
		}
	}
	bad := []string{}
	for _, a := range missing {
		if a != "" && !tl.hasChannel(a) {
			bad = append(bad, a)
		}
	}
	if len(bad) > 0 {
		res.Status = "semantic_no_solution"
		res.Diagnostics = []model.Diagnostic{{Path: "params.custom", Code: "unknown_alias", Message: "unknown channel alias(es): " + strings.Join(bad, ",")}}
		return res
	}

	clock := tl.channelEdges(cfg.Clock)
	type sample struct {
		clk      model.Edge
		known    bool
		value    int
		dataEdge int
		boundary bool
	}
	var events []model.DecodeEvent
	lineBits := map[string][]sample{}
	for _, e := range clock {
		after, _ := tl.levelAfter(cfg.Clock, e.Picoseconds, e.Seq)
		isRise := after == 1
		if (cfg.SampleEdge == "rising") != isRise {
			continue
		}
		if g := tl.gapAt(e.Picoseconds); g != nil {
			events = append(events, model.DecodeEvent{Code: "data_gap", AtPs: &e.Picoseconds, EdgeSeqs: []int{e.Seq},
				Severity: "error", Message: "custom frame crosses a data gap; not stitched across the gap",
				Data: map[string]any{"from_ps": g.FromPs, "to_ps": g.ToPs}})
			continue
		}
		for _, ln := range cfg.Lines {
			before, bseq, known := tl.levelAt(ln, e.Picoseconds, e.Seq)
			boundary := false
			for _, oe := range tl.channelEdges(ln) {
				if oe.Picoseconds == e.Picoseconds && oe.Seq != e.Seq {
					boundary = true
				}
			}
			if boundary {
				events = append(events, model.DecodeEvent{Code: "boundary_sampling", AtPs: &e.Picoseconds, EdgeSeqs: []int{e.Seq},
					Severity: "warning", Message: "custom data/clock edges coincide at sample point"})
			}
			sm := sample{clk: e, known: known, dataEdge: bseq, boundary: boundary}
			if known {
				sm.value = before
			}
			lineBits[ln] = append(lineBits[ln], sm)
		}
	}
	order := cfg.BitOrder
	if order == "" {
		order = "msb"
	}
	var trxs []*model.DecodedTransaction
	for ln, bits := range lineBits {
		key := "custom@" + ln
		groupUID := GroupUID("custom", key)
		var bytes []*model.DecodedByte
		for start := 0; start < len(bits); start += cfg.WordBits {
			end := start + cfg.WordBits
			partial := false
			if end > len(bits) {
				end = len(bits)
				partial = true
			}
			chunk := bits[start:end]
			sb := make([]sampledBit, len(chunk))
			for i, sm := range chunk {
				var d *int
				if sm.known {
					v := sm.value
					d = &v
				}
				sb[i] = sampledBit{clkEdge: sm.clk, data: d, dataEdge: sm.dataEdge, boundary: sm.boundary}
			}
			cands := orderedCandidates(false, groupUID, ln, start/cfg.WordBits, sb, order)
			note := ""
			if partial {
				note = "trailing partial word"
				events = append(events, model.DecodeEvent{Code: "custom_truncated_word", AtPs: &chunk[len(chunk)-1].clk.Picoseconds,
					EdgeSeqs: []int{chunk[len(chunk)-1].clk.Seq}, Severity: "error",
					Message: "custom capture ends mid-word; partial word retained"})
			}
			bytes = append(bytes, &model.DecodedByte{
				UID:       ByteUID("custom", key, ln, start/cfg.WordBits),
				Index:     start / cfg.WordBits,
				Line:      ln,
				Kind:      "data",
				Candidates: cands,
				StartEdge: chunk[0].clk.Seq,
				EndEdge:   chunk[len(chunk)-1].clk.Seq,
				Note:      note,
			})
		}
		state := "complete"
		flags := []string{}
		for _, b := range bytes {
			if len(b.Candidates) > 1 {
				flags = append(flags, "tied_candidates")
			}
		}
		trxs = append(trxs, &model.DecodedTransaction{
			UID: TrxUID("custom", key), GroupUID: groupUID, Bus: "custom", Device: ln,
			Confidence: 1.0, StartEdge: bits[0].clk.Seq, EndEdge: bits[len(bits)-1].clk.Seq,
			State: state, Flags: uniqSorted(flags), Bytes: sortBytes(bytes),
		})
	}
	res.Transactions = sortTransactions(trxs)
	sort.SliceStable(events, func(i, j int) bool {
		ai, aj := events[i].AtPs, events[j].AtPs
		if ai == nil || aj == nil {
			return false
		}
		return *ai < *aj
	})
	res.Events = events
	return res
}

package decoder

import (
	"fmt"

	"edge-decoder-review/internal/model"
)

func analyzeCustom(c model.Capture, bus model.BusConfig, traces map[string]*trace) model.BusAnalysis {
	ba := model.BusAnalysis{BusID: bus.ID, Protocol: "custom_sync"}
	clock := traces[bus.Signals["clk"]]
	data := traces[bus.Signals["data"]]
	sync := traces[bus.Signals["sync"]]
	if clock == nil || data == nil || sync == nil {
		ba.InvalidReasons = append(ba.InvalidReasons, diagnostic("MISSING_BUS_TRACE", "error", "probe.buses."+bus.ID, "custom sync requires clk, data, and sync traces"))
		return ba
	}
	polarities := []string{"rising"}
	if bus.ClockPolarity == "" || bus.ClockPolarity == "unknown" {
		polarities = []string{"rising", "falling"}
	}
	if bus.ClockPolarity == "inverted" {
		polarities = []string{"falling"}
	}
	for _, polarity := range polarities {
		rising := polarity == "rising"
		cand := model.Candidate{ID: stableID("custom-candidate", bus.ID, polarity), Key: polarity, Label: "custom sync sample " + polarity,
			Protocol: "custom_sync", RuleVersion: bus.DecoderVersion, Status: "legal", Confidence: .76,
			Reason: "custom clock polarity retained as candidate when not explicitly fixed"}
		for syncIndex, syncStart := range sync.changes() {
			if !syncStart.logical {
				continue
			}
			end := 1e300
			for _, p := range sync.changes()[syncIndex+1:] {
				if p.logical {
					end = p.t
					break
				}
			}
			edges := sampleEdges(clock, syncStart.t, end, rising)
			if len(edges) == 0 {
				cand.Diagnostics = append(cand.Diagnostics, diagnostic("CUSTOM_CLOCK_MISSING_EDGE", "error", "bus.custom_sync.clk", "sync interval has no sample clock", syncStart.edge.ID))
				continue
			}
			groups, gapIDs := groupClockSamples(edges, c.SampleRate.Value, syncStart.t, end)
			for _, gid := range gapIDs {
				cand.Diagnostics = append(cand.Diagnostics, diagnostic("CUSTOM_DATA_GAP", "warning", "bus.custom_sync.data_gap", "clock gap splits custom data; no transaction is concatenated", gid))
			}
			for gi, group := range groups {
				var value uint8
				var bits, ids []string
				for i, sp := range group {
					level, _ := data.at(sp.t)
					if level {
						value |= 1 << uint(7-i)
						bits = append(bits, "1")
					} else {
						bits = append(bits, "0")
					}
					ids = append(ids, sp.edge.ID)
					if i == 7 {
						break
					}
				}
				status, reason := "decoded", ""
				if len(bits) < 8 {
					status, reason = "incomplete", "CUSTOM_CLOCK_MISSING_EDGE"
				}
				bitText := ""
				for _, bit := range bits {
					bitText += bit
				}
				by := model.Byte{ID: stableID("custom-byte", cand.ID, fmt.Sprint(syncIndex), fmt.Sprint(gi)),
					Index: syncIndex*100 + gi, Status: status, StartTime: q(group[0].t), EndTime: q(group[len(group)-1].t),
					SampleEdgeIDs: ids, ClockEdgeIDs: ids, CandidateID: cand.ID, Bits: bitText, InvalidReason: reason}
				if len(bits) == 8 {
					v := value
					by.Value = &v
					by.Hex = fmt.Sprintf("0x%02X", v)
				}
				cand.Bytes = append(cand.Bytes, by)
				tx := model.Transaction{ID: stableID("custom-tx", cand.ID, syncStart.edge.ID, fmt.Sprint(gi)), BusID: bus.ID,
					Protocol: "custom_sync", Kind: "custom-sync-fragment", StartTime: by.StartTime, EndTime: by.EndTime,
					ByteIDs: []string{by.ID}, StartEdgeID: syncStart.edge.ID, CandidateID: cand.ID,
					Status: ternaryString(len(gapIDs) == 0, "decoded", "data-gap-fragment"), Reason: ternaryString(len(gapIDs) == 0, "", "CUSTOM_DATA_GAP")}
				ba.Transactions = append(ba.Transactions, tx)
			}
			stop := end
			if end == 1e300 {
				stop = edges[len(edges)-1].t
			}
			ba.ValidIntervals = append(ba.ValidIntervals, model.Interval{ID: stableID("custom-interval", bus.ID, syncStart.edge.ID, polarity),
				Start: q(syncStart.t), End: q(stop), Status: "conditional", Reason: "sync active interval; seconds from capture origin", EdgeIDs: []string{syncStart.edge.ID}})
		}
		ba.Candidates = append(ba.Candidates, cand)
	}
	selectCandidate(ba.Candidates)
	return ba
}

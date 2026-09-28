package decoder

import (
	"fmt"
	"strings"

	"edge-decoder-review/internal/model"
)

type spiMode struct{ cpol, cpha int }

func analyzeSPI(c model.Capture, bus model.BusConfig, traces map[string]*trace) model.BusAnalysis {
	ba := model.BusAnalysis{BusID: bus.ID, Protocol: "spi"}
	clock := traces[bus.Signals["sclk"]]
	data := traces[bus.Signals["mosi"]]
	if clock == nil || data == nil || bus.Signals["cs"] == "" {
		ba.InvalidReasons = append(ba.InvalidReasons, diagnostic("MISSING_BUS_TRACE", "error", "probe.buses."+bus.ID, "SPI SCLK, MOSI, or CS trace is missing"))
		return ba
	}
	csAliases := strings.Split(bus.Signals["cs"], ",")
	if len(csAliases) > 1 {
		ba.InvalidReasons = append(ba.InvalidReasons, diagnostic("SPI_MULTI_DEVICE_CS", "warning", "probe.buses."+bus.ID+".signals.cs", "multiple chip-select devices are decoded independently", csAliases...))
	}
	modes := []spiMode{{0, 0}}
	if bus.CPOL == nil || bus.CPHA == nil {
		modes = []spiMode{{0, 0}, {0, 1}, {1, 0}, {1, 1}}
	} else {
		modes = []spiMode{{*bus.CPOL, *bus.CPHA}}
	}
	activeLevels := []bool{false}
	if bus.ActiveChipSelect == "high" {
		activeLevels = []bool{true}
	}
	if bus.ActiveChipSelect == "unknown" || bus.ActiveChipSelect == "" && len(modes) > 1 {
		activeLevels = []bool{false, true}
	}

	for _, mode := range modes {
		for _, active := range activeLevels {
			key := fmt.Sprintf("CPOL=%d,CPHA=%d,CS-active-%s", mode.cpol, mode.cpha, map[bool]string{true: "high", false: "low"}[active])
			cand := model.Candidate{ID: stableID("spi-candidate", bus.ID, key), Key: key, Label: "SPI " + key,
				Protocol: "spi", RuleVersion: bus.DecoderVersion, Status: "legal", Confidence: 0.82,
				Reason: "SPI mode or chip-select polarity is ambiguous; every legal mode is retained"}
			if bus.CPOL != nil && bus.CPHA != nil && bus.ActiveChipSelect != "" && bus.ActiveChipSelect != "unknown" {
				cand.Selected = true
			}
			sampleRising := mode.cpol == mode.cpha
			for deviceIndex, csAlias := range csAliases {
				csAlias = strings.TrimSpace(csAlias)
				cs := traces[csAlias]
				if cs == nil {
					cand.Diagnostics = append(cand.Diagnostics, diagnostic("SPI_MISSING_CS_TRACE", "error", "probe.buses."+bus.ID+".signals.cs", "chip-select trace is missing", csAlias))
					continue
				}
				frames := activeIntervals(cs, active)
				cand.EvidenceEdgeIDs = append(cand.EvidenceEdgeIDs, csAlias)
				for frameIndex, frame := range frames {
					edges := sampleEdges(clock, frame.start.t, frame.end.t, sampleRising)
					if len(edges) == 0 {
						cand.Diagnostics = append(cand.Diagnostics, diagnostic("SPI_CLOCK_MISSING_EDGE", "error", "bus.spi.sclk", "active chip-select interval contains no SPI sample clock", frame.start.edge.ID, frame.end.edge.ID))
						continue
					}
					if !pairedClockEdges(clock, frame.start.t, frame.end.t) {
						cand.Diagnostics = append(cand.Diagnostics, diagnostic("SPI_CLOCK_MISSING_EDGE", "warning", "bus.spi.sclk", "SCLK frame has unpaired edges; multiple candidates remain", frame.start.edge.ID))
					}
					groups, gapIDs := groupClockSamples(edges, c.SampleRate.Value, frame.start.t, frame.end.t)
					for _, id := range gapIDs {
						cand.Diagnostics = append(cand.Diagnostics, diagnostic("SPI_DATA_GAP", "warning", "bus.spi.data_gap", "clock gap splits SPI data; fragments are not concatenated", id))
					}
					for _, dp := range data.changes() {
						if dp.t < frame.start.t || dp.t > frame.end.t {
							continue
						}
						for _, ep := range edges {
							d := dp.t - ep.t
							if d < 0 {
								d = -d
							}
							if c.SampleRate.Valid && d <= 1.5/c.SampleRate.Value {
								cand.Diagnostics = append(cand.Diagnostics, diagnostic("BOUNDARY_SAMPLE", "warning", "bus.spi.boundary", "MOSI changes within 1.5 sample periods of the selected SPI sample edge", dp.edge.ID, ep.edge.ID))
							}
						}
					}
					for gi, group := range groups {
						by := decodeSPIByte(data, group, gi, cand.ID, csAlias, frameIndex*100+deviceIndex*1000)
						cand.Bytes = append(cand.Bytes, by...)
						if len(by) > 0 {
							ids := make([]string, 0, len(by))
							for _, x := range by {
								ids = append(ids, x.ID)
							}
							tx := model.Transaction{ID: stableID("spi-tx", cand.ID, csAlias, fmt.Sprint(frameIndex), fmt.Sprint(gi)),
								BusID: bus.ID, Protocol: "spi", DeviceID: csAlias, Kind: "spi-chip-select-fragment",
								StartTime: by[0].StartTime, EndTime: by[len(by)-1].EndTime, ByteIDs: ids,
								StartEdgeID: frame.start.edge.ID, EndEdgeID: frame.end.edge.ID,
								Status: ternaryString(len(gapIDs) == 0, "decoded", "data-gap-fragment"), Reason: ternaryString(len(gapIDs) == 0, "", "SPI_DATA_GAP"), CandidateID: cand.ID}
							ba.Transactions = append(ba.Transactions, tx)
						}
					}
					ba.ValidIntervals = append(ba.ValidIntervals, model.Interval{ID: stableID("spi-frame", bus.ID, csAlias, frame.start.edge.ID, frame.end.edge.ID),
						Start: q(frame.start.t), End: q(frame.end.t), Status: "conditional", Reason: "CS active interval; seconds from capture origin", EdgeIDs: []string{frame.start.edge.ID, frame.end.edge.ID}})
				}
			}
			cand.EvidenceEdgeIDs = uniqueStrings(cand.EvidenceEdgeIDs)
			ba.Candidates = append(ba.Candidates, cand)
		}
	}
	if len(ba.Candidates) > 1 {
		for i := range ba.Candidates {
			ba.Candidates[i].Confidence -= 0.12
			if ba.Candidates[i].Confidence < .35 {
				ba.Candidates[i].Confidence = .35
			}
		}
	}
	selectCandidate(ba.Candidates)
	return ba
}

type edgeInterval struct{ start, end point }

func activeIntervals(cs *trace, active bool) []edgeInterval {
	var out []edgeInterval
	var start *point
	for _, p := range cs.changes() {
		if p.logical == active && start == nil {
			p := p
			start = &p
		}
		if p.logical != active && start != nil {
			out = append(out, edgeInterval{*start, p})
			start = nil
		}
	}
	return out
}

func sampleEdges(clock *trace, start, end float64, rising bool) []point {
	var out []point
	previous := false
	for _, p := range clock.changes() {
		if p.t >= start && p.t <= end {
			if rising && !previous && p.logical {
				out = append(out, p)
			}
			if !rising && previous && !p.logical {
				out = append(out, p)
			}
		}
		previous = p.logical
	}
	return out
}

func pairedClockEdges(clock *trace, start, end float64) bool {
	count := 0
	for _, p := range clock.changes() {
		if p.t >= start && p.t <= end {
			count++
		}
	}
	return count%2 == 0
}

func decodeSPIByte(data *trace, edges []point, groupIndex int, candidateID, deviceID string, baseIndex int) []model.Byte {
	var out []model.Byte
	for start := 0; start < len(edges); start += 8 {
		end := start + 8
		if end > len(edges) {
			end = len(edges)
		}
		group := edges[start:end]
		var value uint8
		var bits string
		var clockIDs []string
		for i, sp := range group {
			level, _ := data.at(sp.t)
			if level {
				value |= 1 << uint(7-i)
				bits += "1"
			} else {
				bits += "0"
			}
			clockIDs = append(clockIDs, sp.edge.ID)
		}
		status, reason := "decoded", ""
		if len(group) < 8 {
			status, reason = "incomplete", "SPI_CLOCK_MISSING_EDGE"
		}
		by := model.Byte{ID: stableID("spi-byte", candidateID, deviceID, fmt.Sprint(baseIndex+groupIndex), fmt.Sprint(start/8)),
			Index: baseIndex + groupIndex*100 + start/8, Status: status, StartTime: q(group[0].t), EndTime: q(group[len(group)-1].t),
			SampleEdgeIDs: clockIDs, ClockEdgeIDs: clockIDs, CandidateID: candidateID, DeviceID: deviceID,
			Bits: bits, InvalidReason: reason}
		if len(group) == 8 {
			v := value
			by.Value = &v
			by.Hex = fmt.Sprintf("0x%02X", v)
		}
		out = append(out, by)
	}
	return out
}

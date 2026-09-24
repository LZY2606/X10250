package ingest

import (
	"fmt"
	"sort"
	"strings"

	"edgebench/internal/domain"
)

func levelOf(s string) (*int, *domain.Diagnostic) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "h", "high":
		v := 1
		return &v, nil
	case "0", "l", "low":
		v := 0
		return &v, nil
	default:
		return nil, newDiag("level_unknown", domain.SevWarn, "level spelling not recognized; raw level retained", "")
	}
}

func logicalLevel(raw *int, invert bool) *int {
	if raw == nil {
		return nil
	}
	v := *raw
	if invert {
		v ^= 1
	}
	return &v
}

func parseChannels(b *Bundle, res *ParseResult) {
	seen := map[string]bool{}
	for i, bc := range b.Channels {
		path := fmt.Sprintf("/channels/%d", i)
		if strings.TrimSpace(bc.ID) == "" {
			res.Diags = append(res.Diags, fatal("channel_id_missing", "channel requires an id", path))
			continue
		}
		if seen[bc.ID] {
			res.Diags = append(res.Diags, fatal("channel_id_dup", "duplicate channel id "+bc.ID, path+"/id"))
			continue
		}
		seen[bc.ID] = true
		ch := &domain.Channel{ID: bc.ID, Alias: bc.Alias, IdleRaw: bc.Idle, Invert: bc.Invert}
		idle, d := levelOf(bc.Idle)
		if d != nil {
			d.Pos.Path = path + "/idle"
			res.Diags = append(res.Diags, d)
		}
		ch.Idle = logicalLevel(idle, bc.Invert)
		if strings.TrimSpace(bc.Threshold) != "" {
			mag, unit, ok := ParsePhysical(bc.Threshold)
			if !ok {
				dd := fatal("threshold_unparsable", "threshold must be '<number> V|mV': "+bc.Threshold, path+"/threshold")
				res.Diags = append(res.Diags, dd)
			} else {
				raw, dd := RawInt(bc.Threshold, unit, mag, VoltMilli)
				if dd != nil {
					dd.Pos.Path = path + "/threshold"
					res.Diags = append(res.Diags, dd)
				}
				ch.Threshold = raw
			}
		}
		res.Materials.Channels = append(res.Materials.Channels, ch)
	}
}

func fatal(code, msg, path string) *domain.Diagnostic {
	d := newDiag(code, domain.SevFatal, msg, path)
	return d
}

func parseCapture(b *Bundle, res *ParseResult) {
	mag, unit, ok := ParsePhysical(b.Capture.SampleRate)
	if !ok {
		res.Diags = append(res.Diags, fatal("samplerate_unparsable", "capture.sampleRate must be '<number> Hz|kHz|MHz|GHz'", "/capture/sampleRate"))
		return
	}
	raw, d := RawInt(b.Capture.SampleRate, unit, mag, RateHz)
	if d != nil {
		d.Pos.Path = "/capture/sampleRate"
		res.Diags = append(res.Diags, d)
	}
	res.Materials.Capture.SampleRate = raw
}

var protocolLines = map[string][]string{
	"i2c":  {"scl", "sda"},
	"spi":  {"sclk", "mosi"}, // miso/cs optional in lines
	"sync": {"clk", "data"},
}

func parseBuses(b *Bundle, res *ParseResult) {
	chans := res.Materials.ChannelByID()
	seen := map[string]bool{}
	for i, bb := range b.Buses {
		path := fmt.Sprintf("/buses/%d", i)
		if strings.TrimSpace(bb.ID) == "" {
			res.Diags = append(res.Diags, fatal("bus_id_missing", "bus requires an id", path))
			continue
		}
		if seen[bb.ID] {
			res.Diags = append(res.Diags, fatal("bus_id_dup", "duplicate bus id "+bb.ID, path+"/id"))
		}
		seen[bb.ID] = true
		bus := &domain.Bus{
			ID: bb.ID, Protocol: bb.Protocol, Lines: map[string]string{},
			ClockPolarity: bb.ClockPolarity, CPOL: bb.CPOL, CPHA: bb.CPHA,
			BitOrder: bb.BitOrder, WordBits: bb.WordBits, SyncCode: bb.SyncCode,
		}
		req, known := protocolLines[bb.Protocol]
		if !known {
			res.Diags = append(res.Diags, fatal("protocol_unknown", "protocol must be i2c|spi|sync: "+bb.Protocol, path+"/protocol"))
		}
		roles := append(append([]string{}, req...), "miso", "cs0", "cs1", "cs2", "cs3")
		for _, role := range roles {
			if id, ok := bb.Lines[role]; ok {
				if id != "" && chans[id] == nil {
					res.Diags = append(res.Diags, fatal("line_channel_missing",
						fmt.Sprintf("line %s references unknown channel %s", role, id), path+"/lines/"+role))
				}
				bus.Lines[role] = id
			}
		}
		if known {
			for _, role := range req {
				if strings.TrimSpace(bus.Lines[role]) == "" {
					res.Diags = append(res.Diags, fatal("line_required", "protocol "+bb.Protocol+" requires line "+role, path+"/lines/"+role))
				}
			}
		}
		validateBusParams(bus, path, res)
		parseBusGaps(bus, bb, path, res)
		res.Materials.Buses = append(res.Materials.Buses, bus)
	}
}

func validateBusParams(bus *domain.Bus, path string, res *ParseResult) {
	switch bus.Protocol {
	case "i2c":
		if bus.ClockPolarity != "high" && bus.ClockPolarity != "low" {
			res.Diags = append(res.Diags, fatal("i2c_polarity_required",
				"i2c clockPolarity must be declared as high (standard idle-high) or low (inverted capture)", path+"/clockPolarity"))
		}
	case "spi":
		for _, kv := range [][2]string{{"wordBits", bus.WordBits}, {"bitOrder", bus.BitOrder}} {
			if strings.TrimSpace(kv[1]) == "" {
				res.Diags = append(res.Diags, fatal("spi_param_required", "spi requires explicit "+kv[0], path+"/"+kv[0]))
			}
		}
		if bus.WordBits != "" && bus.WordBits != "8" {
			res.Diags = append(res.Diags, fatal("spi_wordbits", "only wordBits=8 supported", path+"/wordBits"))
		}
		if bus.BitOrder != "" && bus.BitOrder != "msb" && bus.BitOrder != "lsb" {
			res.Diags = append(res.Diags, fatal("spi_bitorder", "bitOrder must be msb|lsb", path+"/bitOrder"))
		}
		for name, v := range map[string]string{"cpol": bus.CPOL, "cpha": bus.CPHA} {
			if v != "" && v != "0" && v != "1" {
				res.Diags = append(res.Diags, fatal("spi_mode_value", name+" must be 0|1 or omitted when unknown", path+"/"+name))
			}
		}
	case "sync":
		if bus.ClockPolarity != "high" && bus.ClockPolarity != "low" {
			res.Diags = append(res.Diags, fatal("sync_polarity_required", "sync clockPolarity must be high|low", path+"/clockPolarity"))
		}
		if strings.TrimSpace(bus.SyncCode) == "" {
			res.Diags = append(res.Diags, fatal("sync_code_required", "sync requires an explicit syncCode", path+"/syncCode"))
		}
		if bus.WordBits != "8" || (bus.BitOrder != "msb" && bus.BitOrder != "lsb") {
			res.Diags = append(res.Diags, fatal("sync_frame_params", "sync requires wordBits=8 and bitOrder msb|lsb", path))
		}
	}
}

func parseBusGaps(bus *domain.Bus, bb BundleBus, path string, res *ParseResult) {
	if bb.StretchGap != "" {
		mag, unit, ok := ParsePhysical(bb.StretchGap)
		if !ok {
			res.Diags = append(res.Diags, fatal("stretch_gap_unparsable", "stretchGap must be a time", path+"/stretchGap"))
		} else {
			raw, d := RawInt(bb.StretchGap, unit, mag, TimeValue)
			if d != nil {
				d.Pos.Path = path + "/stretchGap"
				res.Diags = append(res.Diags, d)
			}
			bus.StretchGap = raw
		}
	}
	if bb.DataGap != "" {
		mag, unit, ok := ParsePhysical(bb.DataGap)
		if !ok {
			res.Diags = append(res.Diags, fatal("data_gap_unparsable", "dataGap must be a time", path+"/dataGap"))
		} else {
			raw, d := RawInt(bb.DataGap, unit, mag, TimeValue)
			if d != nil {
				d.Pos.Path = path + "/dataGap"
				res.Diags = append(res.Diags, d)
			}
			bus.DataGap = raw
		}
	}
}

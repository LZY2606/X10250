package store

import (
	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/units"
)

// ImportRequest is the raw import document. Quantities arrive as original
// text + unit and are normalized only on the derived layer; values that
// cannot be converted stay in place with a positional diagnostic.
type ImportRequest struct {
	ProjectName string         `json:"projectName"`
	Label       string         `json:"label"`
	SampleRate  RawQuantity    `json:"sampleRate"`
	TimeUnit    string         `json:"timeUnit"`
	Channels    []InputChannel `json:"channels"`
	Edges       []InputEdge    `json:"edges"`
	Buses       []InputBus     `json:"buses"`
}

type RawQuantity struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type InputChannel struct {
	Alias    string      `json:"alias"`
	Physical string      `json:"physical"`
	Role     string      `json:"role"`
	Inverted bool        `json:"inverted"`
	Voltage  RawQuantity `json:"voltage"`
}

type InputEdge struct {
	Alias string      `json:"alias"`
	Level int         `json:"level"`
	Time  RawQuantity `json:"time"`
}

type InputBus struct {
	BusID          string            `json:"busId"`
	Protocol       string            `json:"protocol"`
	Pins           map[string]string `json:"pins"`
	CPOL           int               `json:"cpol"`
	CPHA           int               `json:"cpha"`
	SPIModeKnown   bool              `json:"spiModeKnown"`
	BitOrder       string            `json:"bitOrder"`
	SampleBoundary string            `json:"sampleBoundary"`
	StretchHoldNs  int64             `json:"stretchHoldNs"`
	GapLimitNs     int64             `json:"gapLimitNs"`
}

// ParsedImport holds normalized but as-yet-unstored material.
type ParsedImport struct {
	Req         ImportRequest
	SampleRate  units.Quantity
	Channels    []domain.Channel
	Edges       []domain.Edge
	Params      []domain.DecoderParams
	Diagnostics []units.Diagnostic
}

// ParseImport validates structure and converts units without dropping the
// raw representation. Incomplete input yields positional diagnostics; it
// never substitutes zero or default values to fake success.
func ParseImport(req ImportRequest, b Budget) (ParsedImport, error) {
	out := ParsedImport{Req: req}
	if req.Label == "" {
		return out, semantic("label", "capture label is required")
	}
	if len(req.Channels) == 0 {
		return out, semantic("channels", "at least one channel is required")
	}
	if b.MaxBytes > 0 && int64(len(req.Edges))*48 > b.MaxBytes {
		return out, budgetErr("edges", "edge payload exceeds byte budget")
	}
	if b.MaxEdges > 0 && len(req.Edges) > b.MaxEdges {
		return out, budgetErr("edges", "edge count exceeds budget")
	}

	out.SampleRate = units.Parse(req.SampleRate.Value, req.SampleRate.Unit, units.KindFrequency)
	if !out.SampleRate.Valid {
		out.Diagnostics = append(out.Diagnostics, units.Diagnostic{
			Path: "sampleRate", Code: "unconvertible_quantity",
			Message: out.SampleRate.Diagnostic,
		})
	}

	aliasSeen := map[string]bool{}
	for i, c := range req.Channels {
		path := "channels[" + itoa(i) + "]"
		if c.Alias == "" {
			return out, semantic(path+".alias", "channel alias is required")
		}
		if aliasSeen[c.Alias] {
			return out, semantic(path+".alias", "duplicate channel alias "+c.Alias)
		}
		aliasSeen[c.Alias] = true
		v := units.Parse(c.Voltage.Value, c.Voltage.Unit, units.KindVoltage)
		if !v.Valid && c.Voltage.Value != "" {
			out.Diagnostics = append(out.Diagnostics, units.Diagnostic{
				Path: path + ".voltage", Code: "unconvertible_quantity", Message: v.Diagnostic,
			})
		}
		out.Channels = append(out.Channels, domain.Channel{
			Alias: c.Alias, Physical: c.Physical, Role: c.Role, Inverted: c.Inverted,
			Voltage: v,
			Raw:     map[string]any{"voltage": c.Voltage, "physical": c.Physical, "role": c.Role},
		})
	}

	// Edges: timestamps convert to picoseconds. Unconvertible timestamps are
	// retained (RawTime/RawUnit, TimeValid=false) with a diagnostic and are
	// excluded from decode rather than collapsed onto zero.
	for i, e := range req.Edges {
		path := "edges[" + itoa(i) + "]"
		if !aliasSeen[e.Alias] {
			return out, corrupt(path+".alias", "edge references unknown channel "+e.Alias)
		}
		if e.Level != 0 && e.Level != 1 {
			return out, corrupt(path+".level", "level must be 0 or 1")
		}
		tq := units.Parse(e.Time.Value, e.Time.Unit, units.KindTime)
		ps, ok := units.Picoseconds(tq)
		ed := domain.Edge{
			Alias: e.Alias, Level: e.Level, TimePS: ps, TimeValid: ok,
			RawTime: e.Time.Value, RawUnit: e.Time.Unit,
			Raw: map[string]any{"time": e.Time, "level": e.Level},
		}
		if !ok {
			out.Diagnostics = append(out.Diagnostics, units.Diagnostic{
				Path: path + ".time", Code: "unconvertible_quantity", Message: tq.Diagnostic,
			})
		}
		out.Edges = append(out.Edges, ed)
	}

	for i, bus := range req.Buses {
		path := "buses[" + itoa(i) + "]"
		if bus.BusID == "" {
			return out, semantic(path+".busId", "bus id is required")
		}
		switch bus.Protocol {
		case "i2c", "spi", "custom":
		default:
			return out, semantic(path+".protocol", "unknown protocol "+bus.Protocol)
		}
		if bus.BitOrder == "" {
			bus.BitOrder = "msb"
		}
		if bus.SampleBoundary == "" {
			bus.SampleBoundary = "pre"
		}
		p := domain.DecoderParams{
			BusID: bus.BusID, Protocol: bus.Protocol,
			CPOL: bus.CPOL, CPHA: bus.CPHA, SPIModeKnown: bus.SPIModeKnown,
			BitOrder: bus.BitOrder, SampleBoundary: bus.SampleBoundary,
			StretchHoldPS:  bus.StretchHoldNs * 1000,
			GapLimitPS:     bus.GapLimitNs * 1000,
			AddressBits:    7,
			DecoderVersion: decoder.RulesVersion,
		}
		p.SCL, p.SDA = bus.Pins["scl"], bus.Pins["sda"]
		p.SCLK, p.MOSI, p.MISO, p.CS = bus.Pins["sclk"], bus.Pins["mosi"], bus.Pins["miso"], bus.Pins["cs"]
		if bus.Protocol == "i2c" && (p.SCL == "" || p.SDA == "") {
			return out, semantic(path+".pins", "i2c requires scl and sda")
		}
		if bus.Protocol == "spi" && (p.SCLK == "" || p.MOSI == "") {
			return out, semantic(path+".pins", "spi requires sclk and mosi")
		}
		out.Params = append(out.Params, p)
	}
	return out, nil
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [24]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		b[n] = '-'
	}
	return string(b[n:])
}

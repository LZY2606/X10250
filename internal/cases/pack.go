package cases

import (
	"sort"

	"edgebench/internal/decoder"
	"edgebench/internal/domain"
	"edgebench/internal/store"
)

// Pack describes the seed material for a fresh review workspace.
type Pack struct {
	Name   string
	Import store.ImportRequest
	Seed   SeedActions
}

// SeedActions are replayed after import to tell the full narrative: one
// parameter revision, one localized source change, and one conflicting
// human decision.
type SeedActions struct {
	ParamBus      string
	ParamRevision domain.DecoderParams
	PatchLabel    string
	Patch         store.PatchMutation
	AdoptFirst    string // candidate group selector (mode label substring)
	AdoptSecond   string
}

// Build constructs the offline case pack with deterministic edges.
func Build() Pack {
	b := &edgeBuilder{}
	buildI2C(b)
	spiEnd := buildSPI(b, 600_000)
	buildCustom(b, spiEnd+5_000)

	// Sort edges by time then alias for deterministic seq. Edge identity is
	// content-addressed at import so this ordering is part of the baseline.
	sort.SliceStable(b.edges, func(i, j int) bool {
		if b.edges[i].Time.Value != b.edges[j].Time.Value || b.edges[i].Time.Unit != b.edges[j].Time.Unit {
			return edgeKeyLess(b.edges[i], b.edges[j])
		}
		return b.edges[i].Alias < b.edges[j].Alias
	})

	req := store.ImportRequest{
		ProjectName: "边沿译码台：逻辑分析波形、I2C/SPI 交易与重叠候选",
		Label:       "capture-v1 baseline",
		SampleRate:  rq("50000000", "Hz"), // 50 MS/s
		TimeUnit:    "ns",
		Channels: []store.InputChannel{
			{Alias: "SCL", Physical: "P0", Role: "scl", Voltage: rq("3.3", "V")},
			{Alias: "SDA", Physical: "P1", Role: "sda", Voltage: rq("3.3", "V")},
			{Alias: "SCK", Physical: "P2", Role: "sclk", Voltage: rq("3.3", "V")},
			{Alias: "MOSI", Physical: "P3", Role: "mosi", Voltage: rq("3.3", "V")},
			{Alias: "MISO", Physical: "P4", Role: "miso", Voltage: rq("3.3", "V")},
			{Alias: "CS0", Physical: "P5", Role: "cs", Voltage: rq("3.3", "V")},
			{Alias: "CS1", Physical: "P6", Role: "cs", Voltage: rq("3.3", "V")},
			{Alias: "CCK", Physical: "P7", Role: "sclk", Voltage: rq("3.3", "V")},
			{Alias: "CDAT", Physical: "P8", Role: "mosi", Voltage: rq("3.3", "V")},
		},
		Edges: b.edges,
		Buses: []store.InputBus{
			{BusID: "i2c0", Protocol: "i2c",
				Pins:           map[string]string{"scl": "SCL", "sda": "SDA"},
				SampleBoundary: "pre", StretchHoldNs: 4000, GapLimitNs: 30000},
			{BusID: "spi0", Protocol: "spi",
				Pins:     map[string]string{"sclk": "SCK", "mosi": "MOSI", "miso": "MISO", "cs": "CS0"},
				BitOrder: "msb", SPIModeKnown: false, GapLimitNs: 1600},
			{BusID: "cust0", Protocol: "custom",
				Pins:     map[string]string{"sclk": "CCK", "mosi": "CDAT"},
				BitOrder: "msb", CPOL: 0, CPHA: 0, GapLimitNs: 4000},
		},
	}

	// Parameter revision: pin SPI to mode 0 (clock polarity/phase decision).
	rev := domain.DecoderParams{
		BusID: "spi0", Protocol: "spi",
		SCLK: "SCK", MOSI: "MOSI", MISO: "MISO", CS: "CS0",
		CPOL: 0, CPHA: 0, SPIModeKnown: true, BitOrder: "msb",
		SampleBoundary: "pre", GapLimitPS: 1_600_000,
		AddressBits: 7, DecoderVersion: decoder.RulesVersion,
	}

	return Pack{
		Name:   req.ProjectName,
		Import: req,
		Seed: SeedActions{
			ParamBus:      "spi0",
			ParamRevision: rev,
			PatchLabel:    "capture-v2 SDA inversion corrected",
			Patch:         store.ChannelInversionMutation{Alias: "SDA", Inverted: false, Bus: "i2c0"},
		},
	}
}

func edgeKeyLess(a, c store.InputEdge) bool {
	// Compare numeric ns values.
	var ai, ci int64
	ai = parseNS(a.Time.Value)
	ci = parseNS(c.Time.Value)
	return ai < ci
}

func parseNS(s string) int64 {
	var v int64
	neg := false
	i := 0
	if i < len(s) && s[i] == '-' {
		neg = true
		i++
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			break
		}
		v = v*10 + int64(s[i]-'0')
	}
	if neg {
		return -v
	}
	return v
}

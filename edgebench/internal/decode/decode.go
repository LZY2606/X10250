// Package decode contains the server-side bus reassembly rules: I2C, SPI and
// the custom synchronous protocol. Rules are versioned (model.DecoderVersion)
// and callable directly from tests, without a browser.
package decode

import (
	"fmt"

	"edgebench/internal/model"
)

// Options controls one derivation.
type Options struct {
	MaxEdges int
	Strict   bool // fatal import diagnostics surface as typed errors
}

// DefaultOptions used by the application.
func DefaultOptions() Options { return Options{MaxEdges: MaxEdgesDefault} }

// Decode derives an immutable, content-addressed report from capture material
// and one parameter revision. It never mutates inputs.
func Decode(c *model.Capture, p *model.Params, opt Options) (*model.Report, error) {
	if opt.MaxEdges <= 0 {
		opt.MaxEdges = MaxEdgesDefault
	}
	rawCount := 0
	for _, re := range c.RawEdges {
		if re.OK {
			rawCount++
		}
	}
	if rawCount > opt.MaxEdges {
		return nil, model.NewBudgetExhausted("EB-BUDGET-EDGES",
			fmt.Sprintf("edge budget exhausted: %d edges > limit %d", rawCount, opt.MaxEdges))
	}
	for _, d := range c.ImportDiagnostics {
		if d.Severity == model.SevFatal && opt.Strict {
			return nil, model.NewFormatCorrupt(d.Code, d.Message, d.Line)
		}
	}

	all, _ := Normalize(c)
	tl := buildTimelines(c, all)

	r := &model.Report{
		CaptureID:      c.ID,
		ParamsID:       p.ID,
		DecoderVersion: model.DecoderVersion,
		BusByName:      map[string]*model.BusReport{},
	}
	r.InputDigest = inputDigest(c, p)
	r.ID = "rep/" + r.InputDigest

	for _, name := range c.BusOrder {
		bus := c.Buses[name]
		br := &model.BusReport{Name: name, Kind: bus.Kind}
		switch bus.Kind {
		case "i2c":
			decodeI2C(c, p, bus, tl, br)
		case "spi":
			decodeSPI(c, p, bus, tl, br)
		case "sync":
			decodeSync(c, p, bus, tl, br)
		}
		r.Buses = append(r.Buses, br)
		r.BusByName[name] = br
		r.Diagnostics = append(r.Diagnostics, br.Diagnostics...)
	}
	// capture-level diagnostics are preserved on every report
	r.Diagnostics = append(append([]model.Diagnostic{}, c.ImportDiagnostics...), r.Diagnostics...)
	if len(c.BusOrder) == 0 {
		r.IncompleteInput = true
	}
	assignIDs(c, p, r)
	return r, nil
}

func inputDigest(c *model.Capture, p *model.Params) string {
	body := map[string]any{
		"capture":  c.ID,
		"raw":      c.RawText,
		"params":   p.ID,
		"decoder":  model.DecoderVersion,
	}
	return model.Hash12(body)
}

// EffectiveGap resolves the gap threshold for a bus with a declared default.
func EffectiveGap(c *model.Capture, p *model.Params, bus string) int64 {
	if v, ok := p.GapNS[bus]; ok {
		return v
	}
	if c.GapNS != nil {
		return *c.GapNS
	}
	return 100_000 // 100 us domain default
}

func effectiveStretch(c *model.Capture) int64 {
	if c.StretchNS != nil {
		return *c.StretchNS
	}
	return 5_000 // 5 us
}

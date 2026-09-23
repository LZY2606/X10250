// Package decode contains the server-side bus reassembly rules for I2C, SPI
// and custom synchronous captures. Every rule lives here (not in the browser)
// so storage and HTTP tests can call it directly.
package decode

import (
	"encoding/json"

	"edgebench/internal/model"
)

// DecoderVersion returns the decoder version string for a bus.
func DecoderVersion(bus string) string {
	switch bus {
	case "i2c":
		return i2cDecoderVersion
	case "spi":
		return spiDecoderVersion
	case "custom":
		return customDecoderVersion
	}
	return ""
}

// HashCapture / HashParam produce stable input summaries.
func HashCapture(c *model.Capture) string {
	if c == nil {
		return ""
	}
	type slim struct {
		UID           string             `json:"uid"`
		SampleRateRaw string             `json:"sample_rate_raw"`
		TimeBase      string             `json:"time_base"`
		SourceFormat  string             `json:"source_format"`
		Channels      []model.ChannelDef `json:"channels"`
		Edges         []model.Edge       `json:"edges"`
		Gaps          []model.Gap        `json:"gaps"`
	}
	return hashObject(slim{UID: c.UID, SampleRateRaw: c.SampleRateRaw, TimeBase: c.TimeBase,
		SourceFormat: c.SourceFormat, Channels: c.Channels, Edges: c.Edges, Gaps: c.Gaps})
}

func HashParam(p *model.ParamVersion) string {
	type slim struct {
		Bus     string          `json:"bus"`
		Version string          `json:"decoder_version"`
		Payload json.RawMessage `json:"payload"`
	}
	var payload any
	switch {
	case p.I2C != nil:
		payload = p.I2C
	case p.SPI != nil:
		payload = p.SPI
	case p.Custom != nil:
		payload = p.Custom
	}
	b, _ := json.Marshal(payload)
	return hashObject(slim{Bus: p.Bus, Version: p.DecoderVersion, Payload: b})
}

// Run decodes a capture under a parameter version. The result is immutable and
// carries its complete input summary.
func Run(c *model.Capture, p *model.ParamVersion) *model.DecodeResult {
	var res *model.DecodeResult
	stretch := int64(0)
	switch p.Bus {
	case "i2c":
		if p.I2C == nil {
			return semantic("params", "missing_payload", "parameter version has no i2c payload")
		}
		if p.I2C.StretchMinRaw != "" {
			// Stretch threshold is optional; invalid values are diagnostics.
			res = DecodeI2C(c, *p.I2C, 0)
		} else {
			res = DecodeI2C(c, *p.I2C, stretch)
		}
	case "spi":
		if p.SPI == nil {
			return semantic("params", "missing_payload", "parameter version has no spi payload")
		}
		res = DecodeSPI(c, *p.SPI)
	case "custom":
		if p.Custom == nil {
			return semantic("params", "missing_payload", "parameter version has no custom payload")
		}
		res = DecodeCustom(c, *p.Custom)
	default:
		return semantic("params", "unknown_bus", "bus must be i2c, spi or custom")
	}
	res.Input = model.InputSummary{
		CaptureUID:     captureUID(c),
		CaptureHash:    HashCapture(c),
		ParamUID:       p.UID,
		ParamHash:      HashParam(p),
		DecoderVersion: p.DecoderVersion,
	}
	return res
}

func captureUID(c *model.Capture) string {
	if c == nil {
		return ""
	}
	return c.UID
}

func semantic(path, code, msg string) *model.DecodeResult {
	return &model.DecodeResult{Status: "semantic_no_solution",
		Diagnostics: []model.Diagnostic{{Path: path, Code: code, Message: msg}}}
}

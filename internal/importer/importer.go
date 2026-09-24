package importer

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"edge-decode-station/internal/model"
)

type Limits struct {
	MaxProbes int
	MaxEdges  int
}

var DefaultLimits = Limits{MaxProbes: 32, MaxEdges: 200000}

type rawCapture struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"`
	SampleRate    json.RawMessage   `json:"sample_rate"`
	TimeBase      string            `json:"time_base"`
	TimeOrigin    json.RawMessage   `json:"time_origin"`
	Probes        []json.RawMessage `json:"probes"`
	Edges         []json.RawMessage `json:"edges"`
	InitialLevels map[string]int    `json:"initial_levels"`
	ValidIntervals []json.RawMessage `json:"valid_intervals"`
	Metadata      map[string]string `json:"metadata"`
}

func ParseImport(data []byte, limits Limits) (model.ImportRequest, error) {
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	var req struct {
		ProjectID  string            `json:"project_id"`
		Name       string            `json:"name"`
		Captures   []json.RawMessage `json:"captures"`
		Parameters []json.RawMessage `json:"parameters"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return model.ImportRequest{}, fmt.Errorf("%w: %v", model.ErrFormatCorrupt, err)
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.Name) == "" {
		return model.ImportRequest{}, diag(model.ErrFormatCorrupt, "$", "project_id and name are required", nil)
	}
	out := model.ImportRequest{ProjectID: stableID("project", req.ProjectID), Name: req.Name, SourcePath: "$"}
	capIDs := map[string]bool{}
	for i, raw := range req.Captures {
		cap, errs := parseCapture(fmt.Sprintf("$.captures[%d]", i), raw, limits)
		if len(errs) > 0 {
			return model.ImportRequest{}, &model.DiagnosticSetError{Diagnostics: errs}
		}
		if capIDs[cap.ID] {
			return model.ImportRequest{}, diag(model.ErrSemanticUnsolvable, fmt.Sprintf("$.captures[%d].id", i), "duplicate capture id", raw)
		}
		capIDs[cap.ID] = true
		out.Captures = append(out.Captures, cap)
	}
	for i, raw := range req.Parameters {
		params, errs := parseParameters(fmt.Sprintf("$.parameters[%d]", i), raw, capIDs)
		if len(errs) > 0 {
			return model.ImportRequest{}, &model.DiagnosticSetError{Diagnostics: errs}
		}
		out.Parameters = append(out.Parameters, params)
	}
	return out, nil
}

func parseCapture(pos string, data []byte, limits Limits) (model.Capture, []model.Diagnostic) {
	var raw rawCapture
	if err := json.Unmarshal(data, &raw); err != nil {
		return model.Capture{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	errs := []model.Diagnostic{}
	if strings.TrimSpace(raw.ID) == "" {
		errs = append(errs, diagnostic("MISSING_ID", "error", pos+".id", "capture id is required", nil))
	}
	if raw.Kind != "digital-channel" {
		errs = append(errs, diagnostic("UNSUPPORTED_KIND", "error", pos+".kind", "only digital-channel is supported", data))
	}
	if len(raw.Probes) > limits.MaxProbes {
		return model.Capture{}, []model.Diagnostic{diagnostic("RESOURCE_EXHAUSTED", "error", pos+".probes", "probe count exceeds budget", data)}
	}
	if len(raw.Edges) > limits.MaxEdges {
		return model.Capture{}, []model.Diagnostic{diagnostic("RESOURCE_EXHAUSTED", "error", pos+".edges", "edge count exceeds budget", data)}
	}
	if len(errs) > 0 {
		return model.Capture{}, errs
	}
	capture := model.Capture{ID: stableID("capture", raw.ID), Kind: raw.Kind, TimeBase: raw.TimeBase, InitialLevels: raw.InitialLevels, Metadata: raw.Metadata, SourcePosition: pos}
	var ok bool
	capture.SampleRate, capture.SampleRateHz, ok = parseQuantity(raw.SampleRate, pos+".sample_rate", "Hz")
	if !ok {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".sample_rate", "sample_rate must be a positive Hz value", raw.SampleRate))
	}
	capture.TimeOrigin, origin, originOK := parseQuantity(raw.TimeOrigin, pos+".time_origin", "ns")
	capture.OriginNS = int64(origin)
	if !originOK {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".time_origin", "time_origin must be ns", raw.TimeOrigin))
	}
	probeByChannel := map[string]bool{}
	for i, p := range raw.Probes {
		probe, pErrs := parseProbe(capture.ID, fmt.Sprintf("%s.probes[%d]", pos, i), p)
		errs = append(errs, pErrs...)
		if probeByChannel[probe.Channel] {
			errs = append(errs, diagnostic("DUPLICATE_CHANNEL", "error", fmt.Sprintf("%s.probes[%d].channel", pos, i), "duplicate channel", p))
		}
		probeByChannel[probe.Channel] = true
		capture.Probes = append(capture.Probes, probe)
	}
	var lastTime int64 = math.MinInt64
	edgeKeys := map[string]bool{}
	for i, e := range raw.Edges {
		edge, eErrs := parseEdge(capture.ID, i, fmt.Sprintf("%s.edges[%d]", pos, i), e)
		errs = append(errs, eErrs...)
		if edge.TimeNS < lastTime {
			errs = append(errs, diagnostic("NON_MONOTONIC_EDGE", "error", fmt.Sprintf("%s.edges[%d].time", pos, i), "edges must be ordered by time", e))
			}
		lastTime = edge.TimeNS
		key := edge.Channel + ":" + strconv.FormatInt(edge.TimeNS, 10)
		if edgeKeys[key] {
			errs = append(errs, diagnostic("DUPLICATE_EDGE", "error", fmt.Sprintf("%s.edges[%d]", pos, i), "duplicate channel edge at same time", e))
		}
		edgeKeys[key] = true
		if !probeByChannel[edge.Channel] {
			errs = append(errs, diagnostic("UNKNOWN_CHANNEL", "error", fmt.Sprintf("%s.edges[%d].channel", pos, i), "edge channel has no probe configuration", e))
		}
		capture.Edges = append(capture.Edges, edge)
	}
	for i, interval := range raw.ValidIntervals {
		iv, ivErrs := parseInterval(capture.ID, fmt.Sprintf("%s.valid_intervals[%d]", pos, i), interval)
		errs = append(errs, ivErrs...)
		capture.ValidIntervals = append(capture.ValidIntervals, iv)
	}
	return capture, errs
}

func parseProbe(captureID, pos string, data json.RawMessage) (model.Probe, []model.Diagnostic) {
	var p struct {
		Channel   string          `json:"channel"`
		Alias     string          `json:"alias"`
		Role      string          `json:"role"`
		Inverted  bool            `json:"inverted"`
		Threshold json.RawMessage `json:"threshold"`
		Scale     json.RawMessage `json:"scale"`
		Offset    json.RawMessage `json:"offset"`
	}
	var errs []model.Diagnostic
	if err := json.Unmarshal(data, &p); err != nil {
		return model.Probe{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	if strings.TrimSpace(p.Channel) == "" {
		errs = append(errs, diagnostic("MISSING_CHANNEL", "error", pos+".channel", "channel is required", data))
	}
	if strings.TrimSpace(p.Alias) == "" {
		errs = append(errs, diagnostic("MISSING_ALIAS", "error", pos+".alias", "channel alias is required", data))
	}
	if !validRole(p.Role) {
		errs = append(errs, diagnostic("UNKNOWN_ROLE", "error", pos+".role", "unsupported probe role", data))
	}
	threshold, thresholdV, ok := parseQuantity(p.Threshold, pos+".threshold", "V")
	if !ok {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".threshold", "threshold must be a positive voltage", p.Threshold))
	}
	scale, scaleV, scaleOK := parseQuantity(p.Scale, pos+".scale", "V/div")
	if !scaleOK || scaleV == 0 {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".scale", "scale must be a voltage per division", p.Scale))
	}
	offset, _, offsetOK := parseOptionalQuantity(p.Offset, pos+".offset", "V")
	if !offsetOK {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".offset", "offset must be a voltage", p.Offset))
	}
	return model.Probe{Channel: p.Channel, Alias: p.Alias, Role: p.Role, Inverted: p.Inverted, Threshold: threshold, Scale: scale, Offset: offset}, errs
}

func parseEdge(captureID string, index int, pos string, data json.RawMessage) (model.Edge, []model.Diagnostic) {
	var e struct {
		Channel string          `json:"channel"`
		Time    json.RawMessage `json:"time"`
		Level   *int            `json:"level"`
	}
	var errs []model.Diagnostic
	if err := json.Unmarshal(data, &e); err != nil {
		return model.Edge{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	if strings.TrimSpace(e.Channel) == "" {
		errs = append(errs, diagnostic("MISSING_CHANNEL", "error", pos+".channel", "channel is required", data))
	}
	timeQ, timeV, ok := parseQuantity(e.Time, pos+".time", "ns")
	if !ok || timeV < 0 {
		errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".time", "edge time must be a non-negative nanosecond value", e.Time))
	}
	if e.Level == nil || (*e.Level != 0 && *e.Level != 1) {
		errs = append(errs, diagnostic("INVALID_LEVEL", "error", pos+".level", "raw level must be 0 or 1", data))
	}
	if e.Level == nil {
		return model.Edge{}, errs
	}
	return model.Edge{ID: fmt.Sprintf("%s/edge/%s/%06d", captureID, e.Channel, index), Channel: e.Channel, Index: index, Time: timeQ, TimeNS: int64(math.Round(timeV)), Level: *e.Level, Position: pos}, errs
}

func parseInterval(captureID, pos string, data json.RawMessage) (model.Interval, []model.Diagnostic) {
	var iv struct {
		Start json.RawMessage `json:"start"`
		End   json.RawMessage `json:"end"`
		Valid *bool           `json:"valid"`
		Reason string         `json:"reason"`
	}
	if err := json.Unmarshal(data, &iv); err != nil {
		return model.Interval{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	var errs []model.Diagnostic
	startQ, start, startOK := parseQuantity(iv.Start, pos+".start", "ns")
	endQ, end, endOK := parseQuantity(iv.End, pos+".end", "ns")
	if !startOK || start < 0 { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".start", "start must be ns", iv.Start)) }
	if !endOK || end < start { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".end", "end must follow start in ns", iv.End)) }
	if iv.Valid == nil { errs = append(errs, diagnostic("MISSING_VALIDITY", "error", pos+".valid", "interval validity is required", data)) }
	if len(errs) > 0 { return model.Interval{}, errs }
	return model.Interval{ID: fmt.Sprintf("%s/interval/%s-%s", captureID, strconv.FormatInt(int64(start), 10), strconv.FormatInt(int64(end), 10)), CaptureID: captureID, StartNS: int64(start), EndNS: int64(end), Start: string(startQ.Raw), End: string(endQ.Raw), Unit: "ns", Valid: *iv.Valid, Reason: iv.Reason}, nil
}

func parseQuantity(raw json.RawMessage, pos, expectedUnit string) (model.RawQuantity, float64, bool) {
	q := model.RawQuantity{Raw: raw, Unit: expectedUnit}
	if len(raw) == 0 {
		return q, 0, false
	}
	var obj struct {
		Value *float64 `json:"value"`
		Unit  string   `json:"unit"`
		Raw   json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.Value != nil {
		if !unitCompatible(obj.Unit, expectedUnit) || *obj.Value <= 0 || math.IsNaN(*obj.Value) || math.IsInf(*obj.Value, 0) {
			return q, 0, false
		}
		q.Value = obj.Value
		q.Unit = obj.Unit
		return q, *obj.Value, true
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) {
			return q, 0, false
		}
		q.Value = &number
		return q, number, true
	}
	return q, 0, false
}

func unitCompatible(actual, expected string) bool {
	if actual == expected { return true }
	compat := map[string][]string{"ns": {"ns", "nanosecond", "nanoseconds"}, "Hz": {"Hz", "hz", "S/s", "Sa/s", "samples/s"}}
	for _, unit := range compat[expected] {
		if actual == unit { return true }
	}
	return false
}

func parseQuantityNS(raw json.RawMessage, pos string, positive bool) (model.RawQuantity, int64, bool) {
	q, v, ok := parseQuantity(raw, pos, "ns")
	if !ok { return q, 0, false }
	if positive && v <= 0 { return q, 0, false }
	return q, int64(math.Round(v)), true
}

func parseOptionalQuantity(raw json.RawMessage, pos, expectedUnit string) (model.RawQuantity, float64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return model.RawQuantity{Raw: raw, Unit: expectedUnit}, 0, true
	}
	return parseQuantity(raw, pos, expectedUnit)
}

func validRole(role string) bool {
	switch role {
	case "scl", "sda", "sclk", "mosi", "miso", "cs", "clock", "data", "frame":
		return true
	default:
		return false
	}
}

func parseParameters(pos string, data json.RawMessage, captures map[string]bool) (model.Parameters, []model.Diagnostic) {
	var head struct {
		ID       string `json:"id"`
		Revision int    `json:"revision"`
		I2C      json.RawMessage `json:"i2c"`
		SPI      json.RawMessage `json:"spi"`
		Custom   json.RawMessage `json:"custom"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return model.Parameters{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	var bodies int
	if len(head.I2C) > 0 && string(head.I2C) != "null" { bodies++ }
	if len(head.SPI) > 0 && string(head.SPI) != "null" { bodies++ }
	if len(head.Custom) > 0 && string(head.Custom) != "null" { bodies++ }
	var errs []model.Diagnostic
	if strings.TrimSpace(head.ID) == "" {
		errs = append(errs, diagnostic("MISSING_ID", "error", pos+".id", "parameters id is required", data))
	}
	if head.Revision <= 0 {
		errs = append(errs, diagnostic("INVALID_REVISION", "error", pos+".revision", "revision must be positive", data))
	}
	if bodies != 1 {
		errs = append(errs, diagnostic("AMBIGUOUS_PROTOCOL", "error", pos, "exactly one protocol parameters object is required", data))
	}
	if len(errs) > 0 { return model.Parameters{}, errs }
	out := model.Parameters{ID: stableID("parameters", head.ID), Revision: head.Revision}
	switch {
	case len(head.I2C) > 0:
		i2c, ierrs := parseI2C(pos+".i2c", head.I2C, captures)
		out.I2C = &i2c
		errs = append(errs, ierrs...)
	case len(head.SPI) > 0:
		spi, serrs := parseSPI(pos+".spi", head.SPI, captures)
		out.SPI = &spi
		errs = append(errs, serrs...)
	default:
		custom, cerrs := parseCustom(pos+".custom", head.Custom, captures)
		out.Custom = &custom
		errs = append(errs, cerrs...)
	}
	return out, errs
}

func requireCapture(pos, capture string, captures map[string]bool) []model.Diagnostic {
	if !captures[stableID("capture", capture)] {
		return []model.Diagnostic{diagnostic("UNKNOWN_CAPTURE", "error", pos+".capture", "parameters reference an unknown capture", nil)}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate { return true
		}
	}
	return false
}

func parseI2C(pos string, data json.RawMessage, captures map[string]bool) (model.I2CParameters, []model.Diagnostic) {
	var p struct {
		Capture string `json:"capture"`
		SCL string `json:"scl"`
		SDA string `json:"sda"`
		ClockPolarity string `json:"clock_polarity"`
		BoundarySampling string `json:"boundary_sampling"`
		DataGap json.RawMessage `json:"data_gap"`
		ClockStretchLimit json.RawMessage `json:"clock_stretch_limit"`
		AddressBits int `json:"address_bits"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return model.I2CParameters{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	var errs []model.Diagnostic
	errs = append(errs, requireCapture(pos, p.Capture, captures)...)
	if p.SCL == "" { errs = append(errs, diagnostic("MISSING_SCL", "error", pos+".scl", "scl channel is required", data)) }
	if p.SDA == "" { errs = append(errs, diagnostic("MISSING_SDA", "error", pos+".sda", "sda channel is required", data)) }
	if p.SCL == p.SDA { errs = append(errs, diagnostic("CHANNEL_CONFLICT", "error", pos, "scl and sda must differ", data)) }
	if !oneOf(p.ClockPolarity, "rising-data-falling", "falling-data-rising") { errs = append(errs, diagnostic("UNKNOWN_CLOCK_POLARITY", "error", pos+".clock_polarity", "unsupported i2c clock interpretation", data)) }
	if !oneOf(p.BoundarySampling, "mid-bit", "boundary-reject", "boundary-keep") { errs = append(errs, diagnostic("UNKNOWN_BOUNDARY_POLICY", "error", pos+".boundary_sampling", "unsupported boundary policy", data)) }
	gap, gapNS, gapOK := parseQuantityNS(p.DataGap, pos+".data_gap", true)
	if !gapOK { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".data_gap", "data gap must be positive ns", p.DataGap)) }
	stretch, stretchNS, stretchOK := parseQuantityNS(p.ClockStretchLimit, pos+".clock_stretch_limit", true)
	if !stretchOK { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".clock_stretch_limit", "stretch limit must be positive ns", p.ClockStretchLimit)) }
	if p.AddressBits != 7 && p.AddressBits != 10 { errs = append(errs, diagnostic("UNSUPPORTED_ADDRESS_WIDTH", "error", pos+".address_bits", "address_bits must be 7 or 10", data)) }
	return model.I2CParameters{Capture: stableID("capture", p.Capture), SCL: p.SCL, SDA: p.SDA, ClockPolarity: p.ClockPolarity, BoundarySampling: p.BoundarySampling, DataGap: gap, DataGapNS: gapNS, ClockStretchLimit: stretch, ClockStretchLimitNS: stretchNS, AddressBits: p.AddressBits}, errs
}

func parseSPI(pos string, data json.RawMessage, captures map[string]bool) (model.SPIParameters, []model.Diagnostic) {
	var p struct {
		Capture string `json:"capture"`
		SCLK string `json:"sclk"`
		MOSI string `json:"mosi"`
		MISO string `json:"miso"`
		Devices []model.SPIDeviceSelect `json:"devices"`
		CPOL *int `json:"cpol"`
		CPHA *int `json:"cpha"`
		ModeText string `json:"mode_text"`
		BoundarySampling string `json:"boundary_sampling"`
		BitOrder string `json:"bit_order"`
		DataGap json.RawMessage `json:"data_gap"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return model.SPIParameters{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	var errs []model.Diagnostic
	errs = append(errs, requireCapture(pos, p.Capture, captures)...)
	if p.SCLK == "" { errs = append(errs, diagnostic("MISSING_SCLK", "error", pos+".sclk", "sclk channel is required", data)) }
	if p.MOSI == "" && p.MISO == "" { errs = append(errs, diagnostic("MISSING_DATA_LINE", "error", pos, "mosi", "at least one data line is required", data)) }
	if len(p.Devices) == 0 { errs = append(errs, diagnostic("MISSING_DEVICE_SELECT", "error", pos+".devices", "at least one device select is required", data)) }
	seen := map[string]bool{}
	for i, device := range p.Devices {
		if device.Channel == "" || device.Device == "" || !oneOf(device.ActiveLevel, "low", "high") {
			errs = append(errs, diagnostic("INVALID_DEVICE_SELECT", "error", fmt.Sprintf("%s.devices[%d]", pos, i), "channel, device and active_level high/low are required", data))
		}
		if seen[device.Channel] { errs = append(errs, diagnostic("DUPLICATE_DEVICE_SELECT", "error", fmt.Sprintf("%s.devices[%d].channel", pos, i), "duplicate chip select channel", data)) }
		seen[device.Channel] = true
	}
	if p.CPOL != nil && (*p.CPOL < 0 || *p.CPOL > 1) { errs = append(errs, diagnostic("INVALID_CPOL", "error", pos+".cpol", "cpol must be 0 or 1", data)) }
	if p.CPHA != nil && (*p.CPHA < 0 || *p.CPHA > 1) { errs = append(errs, diagnostic("INVALID_CPHA", "error", pos+".cpha", "cpha must be 0 or 1", data)) }
	if !oneOf(p.BoundarySampling, "leading", "trailing", "boundary-reject", "boundary-keep") { errs = append(errs, diagnostic("UNKNOWN_BOUNDARY_POLICY", "error", pos+".boundary_sampling", "unsupported boundary policy", data)) }
	if !oneOf(p.BitOrder, "msb-first", "lsb-first") { errs = append(errs, diagnostic("UNKNOWN_BIT_ORDER", "error", pos+".bit_order", "bit_order must be msb-first or lsb-first", data)) }
	gap, gapNS, gapOK := parseQuantityNS(p.DataGap, pos+".data_gap", true)
	if !gapOK { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".data_gap", "data gap must be positive ns", p.DataGap)) }
	return model.SPIParameters{Capture: stableID("capture", p.Capture), SCLK: p.SCLK, MOSI: p.MOSI, MISO: p.MISO, Devices: p.Devices, CPOL: p.CPOL, CPHA: p.CPHA, ModeText: p.ModeText, BoundarySampling: p.BoundarySampling, BitOrder: p.BitOrder, DataGap: gap, DataGapNS: gapNS}, errs
}

func parseCustom(pos string, data json.RawMessage, captures map[string]bool) (model.CustomParameters, []model.Diagnostic) {
	var p struct {
		Capture string `json:"capture"`
		Clock string `json:"clock"`
		Data string `json:"data"`
		Frame string `json:"frame"`
		ActiveEdge string `json:"active_edge"`
		BitOrder string `json:"bit_order"`
		BitsPerFrame int `json:"bits_per_frame"`
		DataGap json.RawMessage `json:"data_gap"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return model.CustomParameters{}, []model.Diagnostic{diagnostic("FORMAT_CORRUPT", "error", pos, err.Error(), data)}
	}
	var errs []model.Diagnostic
	errs = append(errs, requireCapture(pos, p.Capture, captures)...)
	if p.Clock == "" { errs = append(errs, diagnostic("MISSING_CLOCK", "error", pos+".clock", "clock channel is required", data)) }
	if p.Data == "" { errs = append(errs, diagnostic("MISSING_DATA", "error", pos+".data", "data channel is required", data)) }
	if p.Frame == "" { errs = append(errs, diagnostic("MISSING_FRAME", "error", pos+".frame", "frame channel is required", data)) }
	if !oneOf(p.ActiveEdge, "rising", "falling") { errs = append(errs, diagnostic("UNKNOWN_ACTIVE_EDGE", "error", pos+".active_edge", "active edge must be rising or falling", data)) }
	if !oneOf(p.BitOrder, "msb-first", "lsb-first") { errs = append(errs, diagnostic("UNKNOWN_BIT_ORDER", "error", pos+".bit_order", "bit_order must be msb-first or lsb-first", data)) }
	if p.BitsPerFrame <= 0 || p.BitsPerFrame > 64 { errs = append(errs, diagnostic("INVALID_FRAME_WIDTH", "error", pos+".bits_per_frame", "bits per frame must be 1..64", data)) }
	gap, gapNS, gapOK := parseQuantityNS(p.DataGap, pos+".data_gap", true)
	if !gapOK { errs = append(errs, diagnostic("UNCONVERTIBLE_VALUE", "error", pos+".data_gap", "data gap must be positive ns", p.DataGap)) }
	return model.CustomParameters{Capture: stableID("capture", p.Capture), Clock: p.Clock, Data: p.Data, Frame: p.Frame, ActiveEdge: p.ActiveEdge, BitOrder: p.BitOrder, BitsPerFrame: p.BitsPerFrame, DataGap: gap, DataGapNS: gapNS}, errs
}

func stableID(kind, raw string) string { return kind + ":" + strings.TrimSpace(raw) }

func diag(target error, pos, msg string, raw json.RawMessage) error {
	code := "SEMANTIC_UNSOLVABLE"
	if target == model.ErrFormatCorrupt { code = "FORMAT_CORRUPT" }
	if target == model.ErrResourceExhausted { code = "RESOURCE_EXHAUSTED" }
	return &model.DiagnosticSetError{Diagnostics: []model.Diagnostic{diagnostic(code, "error", pos, msg, raw)}}
}

func diagnostic(code, severity, pos, msg string, raw json.RawMessage) model.Diagnostic {
	return model.Diagnostic{Code: code, Severity: severity, Position: pos, Message: msg, Value: raw}
}

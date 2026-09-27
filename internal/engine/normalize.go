package engine

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"edge-decode-bench/internal/domain"
)

func NormalizeCapture(in domain.CaptureInput) (domain.Capture, []domain.Diagnostic) {
	c := domain.Capture{
		ID:          in.ID,
		Name:        strings.TrimSpace(in.Name),
		Description: in.Description,
		SampleRate:  in.SampleRate,
		TimeBase:    in.TimeBase,
	}
	var diags []domain.Diagnostic
	add := func(code, severity, path, msg string) {
		diags = append(diags, domain.Diagnostic{Code: code, Severity: severity, Path: path, Message: msg})
	}
	if c.ID == "" {
		c.ID = domain.StableID("capture", c.Name, c.Description)
	}
	if c.Name == "" {
		add("capture_name_required", "error", "name", "capture name is required")
	}
	if hz, ok, d := parseFrequency(in.SampleRate, "sample_rate"); !ok {
		diags = append(diags, d)
	} else {
		c.SampleRate.Valid = true
		c.SampleRate.ValueHz = hz
	}
	if ns, ok, d := parseTime(in.TimeBase, "time_base"); !ok {
		diags = append(diags, d)
	} else {
		c.TimeBase.Valid = true
		c.TimeBase.ValueNS = ns
	}
	raw, _ := json.Marshal(in)
	c.RawJSON = string(raw)
	probeByID := map[string]domain.Probe{}
	for i, p := range in.Probes {
		path := "probes[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(p.ID) == "" {
			add("probe_id_required", "error", path+".id", "probe id is required")
			continue
		}
		if _, exists := probeByID[p.ID]; exists {
			add("probe_id_duplicate", "error", path+".id", "probe id is duplicated")
		}
		if strings.TrimSpace(p.Channel) == "" {
			add("probe_channel_required", "error", path+".channel", "physical channel is required")
		}
		if p.InitialLevel == domain.LevelUnknown {
			add("probe_initial_level_required", "error", path+".initial_level", "initial level is required")
		}
		alias := p.Alias
		if strings.TrimSpace(alias) == "" {
			alias = p.ID
		}
		probe := domain.Probe{ID: p.ID, Alias: alias, Channel: p.Channel, Inverted: p.Inverted, InitialLevel: p.InitialLevel}
		probeByID[p.ID] = probe
		c.Probes = append(c.Probes, probe)
	}
	for i, b := range in.Buses {
		path := "buses[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(b.ID) == "" {
			add("bus_id_required", "error", path+".id", "bus id is required")
			continue
		}
		proto := strings.ToLower(strings.TrimSpace(b.Protocol))
		if proto != "i2c" && proto != "spi" && proto != "custom_sync" {
			add("bus_protocol_unknown", "error", path+".protocol", "protocol must be i2c, spi, or custom_sync")
		}
		if strings.TrimSpace(b.DecoderVersion) == "" {
			add("decoder_version_required", "error", path+".decoder_version", "decoder version is required")
		}
		if probeByID[b.ClockProbe].ID == "" {
			add("clock_probe_missing", "error", path+".clock_probe", "clock probe does not reference a defined probe")
		}
		if proto != "spi" && probeByID[b.DataProbe].ID == "" {
			add("data_probe_missing", "error", path+".data_probe", "data probe does not reference a defined probe")
		}
		if proto == "spi" {
			if probeByID[b.DataProbe].ID == "" && probeByID[b.DataInProbe].ID == "" {
				add("spi_data_probe_missing", "error", path+".data_probe", "at least one SPI MOSI or MISO probe is required")
			}
			if b.ClockPolarity < -1 || b.ClockPolarity > 1 {
				add("clock_polarity_unknown", "error", path+".clock_polarity", "SPI clock polarity must be 0, 1, or -1 for unknown")
			}
			if b.CPHA < -1 || b.CPHA > 1 {
				add("spi_cpha_unknown", "error", path+".cpha", "SPI CPHA must be 0, 1, or -1 for unknown")
			}
		} else if b.ClockPolarity != 0 && b.ClockPolarity != 1 {
			add("clock_polarity_invalid", "error", path+".clock_polarity", "clock polarity must be 0 or 1")
		}
		boundary := b.SampleBoundary
		if boundary == "" {
			boundary = "after_settle"
		}
		if boundary != "before_settle" && boundary != "after_settle" {
			add("sample_boundary_invalid", "error", path+".sample_boundary", "sample boundary must be before_settle or after_settle")
		}
		pol := b.SelectPolarity
		if pol == 0 && len(b.SelectProbes) > 0 {
			pol = -1
		}
		for j, sp := range b.SelectProbes {
			if probeByID[sp].ID == "" {
				add("select_probe_missing", "error", path+".select_probes["+strconv.Itoa(j)+"]", "select probe does not reference a defined probe")
			}
		}
		alias := b.Alias
		if alias == "" {
			alias = b.ID
		}
		c.Buses = append(c.Buses, domain.Bus{
			ID: b.ID, Alias: alias, Protocol: proto, DecoderVersion: b.DecoderVersion,
			ClockProbe: b.ClockProbe, DataProbe: b.DataProbe, DataInProbe: b.DataInProbe,
			SelectProbes: append([]string(nil), b.SelectProbes...), ClockPolarity: b.ClockPolarity,
			CPHA: b.CPHA, SelectPolarity: pol, SampleBoundary: boundary,
		})
	}
	type pendingEdge struct {
		edge         domain.Edge
		inputOrdinal int
	}
	var pending []pendingEdge
	for i, e := range in.Edges {
		path := "edges[" + strconv.Itoa(i) + "]"
		probe := probeByID[e.Probe]
		if probe.ID == "" {
			add("edge_probe_missing", "error", path+".probe", "edge probe is not defined")
			continue
		}
		if e.Level != domain.LevelLow && e.Level != domain.LevelHigh {
			add("edge_level_invalid", "error", path+".level", "edge level must be 0 or 1")
			continue
		}
		ns, ok, d := parseTime(e.Time, path+".time")
		edge := domain.Edge{Original: e.Time.Original, TimeUnit: e.Time.Unit, ProbeID: e.Probe, Ordinal: i, Valid: ok}
		if ok {
			edge.TimeNS = ns
		} else {
			diags = append(diags, d)
		}
		level := e.Level
		if probe.Inverted {
			level = 1 - level
		}
		edge.Level = level
		if e.ID != "" {
			edge.ID = e.ID
		}
		pending = append(pending, pendingEdge{edge: edge, inputOrdinal: i})
	}
	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].edge.TimeNS != pending[j].edge.TimeNS {
			return pending[i].edge.TimeNS < pending[j].edge.TimeNS
		}
		if pending[i].edge.ProbeID != pending[j].edge.ProbeID {
			return pending[i].edge.ProbeID < pending[j].edge.ProbeID
		}
		return pending[i].inputOrdinal < pending[j].inputOrdinal
	})
	for ord, p := range pending {
		e := p.edge
		if e.ID == "" {
			e.ID = domain.StableID(c.ID, "edge", e.ProbeID, e.Original, int(e.Level), ord)
		}
		e.Ordinal = ord
		c.Edges = append(c.Edges, e)
	}
	return c, diags
}

func parseTime(q domain.Qty, path string) (int64, bool, domain.Diagnostic) {
	if strings.TrimSpace(q.Original) == "" {
		return 0, false, domain.Diagnostic{Code: "quantity_original_required", Severity: "error", Path: path, Message: "original representation is required"}
	}
	unit := strings.TrimSpace(q.Unit)
	if unit == "" {
		unit = inferUnit(q.Original)
	}
	mult, ok := timeMultiplier(unit)
	if !ok {
		return 0, false, domain.Diagnostic{Code: "time_unit_unsupported", Severity: "error", Path: path, Message: "time unit is not ns, us, ms, or s: " + unit}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(q.Original, strings.TrimPrefix(unit, normalizeUnit(unit)))), 64)
	num := strings.TrimSpace(stripUnit(q.Original))
	v, err = strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false, domain.Diagnostic{Code: "time_value_invalid", Severity: "error", Path: path, Message: "time value is not numeric: " + q.Original}
	}
	ns := int64(v*float64(mult) + 0.5)
	if ns < 0 {
		return 0, false, domain.Diagnostic{Code: "time_negative", Severity: "error", Path: path, Message: "time must not be negative"}
	}
	return ns, true, domain.Diagnostic{}
}

func parseFrequency(q domain.Qty, path string) (int64, bool, domain.Diagnostic) {
	if strings.TrimSpace(q.Original) == "" {
		return 0, false, domain.Diagnostic{Code: "quantity_original_required", Severity: "error", Path: path, Message: "original representation is required"}
	}
	unit := strings.TrimSpace(q.Unit)
	if unit == "" {
		unit = inferUnit(q.Original)
	}
	mult, ok := frequencyMultiplier(unit)
	if !ok {
		return 0, false, domain.Diagnostic{Code: "frequency_unit_unsupported", Severity: "error", Path: path, Message: "frequency unit is not Hz, kHz, MHz, or GHz: " + unit}
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(stripUnit(q.Original)), 64)
	if err != nil {
		return 0, false, domain.Diagnostic{Code: "frequency_value_invalid", Severity: "error", Path: path, Message: "frequency value is not numeric: " + q.Original}
	}
	return int64(v*float64(mult) + 0.5), true, domain.Diagnostic{}
}

func timeMultiplier(unit string) (int64, bool) {
	switch normalizeUnit(unit) {
	case "ns":
		return 1, true
	case "us", "µs":
		return 1000, true
	case "ms":
		return 1_000_000, true
	case "s":
		return 1_000_000_000, true
	default:
		return 0, false
	}
}
func frequencyMultiplier(unit string) (int64, bool) {
	switch normalizeUnit(unit) {
	case "hz":
		return 1, true
	case "khz":
		return 1000, true
	case "mhz":
		return 1_000_000, true
	case "ghz":
		return 1_000_000_000, true
	default:
		return 0, false
	}
}
func normalizeUnit(u string) string { return strings.ToLower(strings.TrimSpace(u)) }
func inferUnit(s string) string {
	s = strings.TrimSpace(s)
	for _, u := range []string{"GHz", "MHz", "kHz", "Hz", "ns", "us", "µs", "ms", "s"} {
		if strings.HasSuffix(s, u) {
			return u
		}
	}
	return ""
}
func stripUnit(s string) string {
	u := inferUnit(s)
	if u == "" {
		return s
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), u))
}

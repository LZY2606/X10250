package edge

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ParseImport parses one import document.  It always returns the partially
// parsed capture so callers can retain unconvertible values in place; the
// diagnostics classify the outcome into the three mandated distinct results.
func ParseImport(raw []byte, lim Limits) (Capture, []Diagnostic) {
	if len(raw) > lim.MaxInputBytes {
		return Capture{Format: "edgebench.import/v1"}, []Diagnostic{{
			Code: EBudget, Path: "", Kind: "budget",
			Message: fmt.Sprintf("import payload %d bytes exceeds budget %d", len(raw), lim.MaxInputBytes),
		}}
	}

	var doc importWire
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Capture{Format: "edgebench.import/v1"}, []Diagnostic{{
			Code: EFormat, Path: "$", Kind: "format",
			Message: "document is not valid JSON: " + err.Error(),
		}}
	}

	var diags []Diagnostic
	add := func(code, path, msg string) {
		kind := "semantic"
		if code == EFormat {
			kind = "format"
		}
		diags = append(diags, Diagnostic{Code: code, Path: path, Kind: kind, Message: msg})
	}

	c := Capture{
		Format:     orDefault(doc.Format, "edgebench.import/v1"),
		FormatRev:  doc.FormatRev,
		SourceName: doc.SourceName,
		TimeBase:   orDefault(doc.TimeBase, "ns"),
		Notes:      doc.Notes,
	}
	c.SampleRate = parseQuantity(doc.SampleRate, "Hz", "/sampleRate", add)

	seenCh := map[string]int{}
	for i, p := range doc.Probes {
		path := fmt.Sprintf("/probes/%d", i)
		if strings.TrimSpace(p.Channel) == "" {
			add(EFormat, path+"/channel", "probe channel is required")
			continue
		}
		if prev, dup := seenCh[p.Channel]; dup {
			add(ESemantic, path+"/channel",
				fmt.Sprintf("channel %q already configured at /probes/%d", p.Channel, prev))
		}
		seenCh[p.Channel] = i
		pr := Probe{Channel: p.Channel, Alias: orDefault(p.Alias, p.Channel), Inverted: p.Inverted}
		if p.Threshold != nil {
			pr.Threshold = parseQuantity(*p.Threshold, "V", path+"/threshold", add)
		}
		c.Probes = append(c.Probes, pr)
	}

	for i, e := range doc.Edges {
		if len(c.Edges) >= lim.MaxEdges {
			add(EBudget, fmt.Sprintf("/edges/%d", i),
				fmt.Sprintf("edge budget %d exhausted; remaining edges not ingested", lim.MaxEdges))
			break
		}
		path := fmt.Sprintf("/edges/%d", i)
		if strings.TrimSpace(e.Channel) == "" {
			add(EFormat, path+"/channel", "edge channel is required")
			continue
		}
		if _, ok := seenCh[e.Channel]; !ok {
			add(ESemantic, path+"/channel",
				fmt.Sprintf("edge references channel %q that has no probe configuration", e.Channel))
		}
		lvl := normalizeLevel(e.Level)
		if lvl != "0" && lvl != "1" {
			add(ESemantic, path+"/level", fmt.Sprintf("level must be 0 or 1, got %q", e.Level))
		}
		t := parseQuantity(e.Time, c.TimeBase, path+"/time", add)
		re := RawEdge{Channel: e.Channel, Time: strings.TrimSpace(string(e.Time)), TimeNs: t, Level: lvl}
		if e.Voltage != nil {
			re.Voltage = parseQuantity(*e.Voltage, "V", path+"/voltage", add)
		}
		c.Edges = append(c.Edges, re)
	}

	if len(doc.Edges) == 0 {
		add(ESemantic, "/edges", "capture contains no edges")
	}

	// Equivalent inputs must always produce the same edge order.
	sort.SliceStable(c.Edges, func(i, j int) bool {
		a, b := c.Edges[i], c.Edges[j]
		if a.TimeNs.OK() != b.TimeNs.OK() {
			return a.TimeNs.OK() // convertible edges first
		}
		if a.TimeNs.OK() && a.TimeNs.Value != b.TimeNs.Value {
			return a.TimeNs.Value < b.TimeNs.Value
		}
		if a.Channel != b.Channel {
			return a.Channel < b.Channel
		}
		return a.Level < b.Level
	})
	return c, diags
}

type importWire struct {
	Format     string          `json:"format"`
	FormatRev  string          `json:"formatRev"`
	SourceName string          `json:"sourceName"`
	SampleRate json.RawMessage `json:"sampleRate"`
	TimeBase   string          `json:"timeBase"`
	Probes     []probeWire     `json:"probes"`
	Edges      []edgeWire      `json:"edges"`
	Notes      string          `json:"notes"`
}

type probeWire struct {
	Channel   string          `json:"channel"`
	Alias     string          `json:"alias"`
	Inverted  bool            `json:"inverted"`
	Threshold json.RawMessage `json:"threshold"`
}

type edgeWire struct {
	Channel string          `json:"channel"`
	Time    json.RawMessage `json:"time"`
	Level   string          `json:"level"`
	Voltage json.RawMessage `json:"voltage"`
}

func normalizeLevel(s string) string {
	s = strings.TrimSpace(s)
	switch s {
	case "1", "0":
		return s
	}
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && (f == 0 || f == 1) {
		return strconv.Itoa(int(f))
	}
	return s
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// parseQuantity accepts {value, unit}, a bare number or a string like "12.4us".
func parseQuantity(raw json.RawMessage, defaultUnit, path string, add func(code, p, m string)) Scalar {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		add(ESemantic, path, "value is missing")
		return Scalar{Unit: defaultUnit, Err: "missing"}
	}

	// Structured form.
	if trimmed[0] == '{' {
		var q struct {
			Value *float64 `json:"value"`
			Raw   string   `json:"raw"`
			Unit  string   `json:"unit"`
		}
		if err := json.Unmarshal(raw, &q); err != nil {
			add(EFormat, path, "malformed quantity: "+err.Error())
			return Scalar{Raw: trimmed, Unit: defaultUnit, Err: err.Error()}
		}
		unit := orDefault(q.Unit, defaultUnit)
		if q.Value == nil {
			if q.Raw != "" {
				return parseTextQuantity(q.Raw, defaultUnit, path, add)
			}
			add(ESemantic, path, "quantity.value is required")
			return Scalar{Raw: trimmed, Unit: unit, Err: "missing value"}
		}
		v, err := convertUnit(*q.Value, unit)
		if err != nil {
			add(ESemantic, path, err.Error())
		}
		return Scalar{Raw: trimmed, Unit: unit, Value: v, Err: errText(err)}
	}

	// Bare number: normalize from the declared default unit.
	if f, err := strconv.ParseFloat(strings.Trim(trimmed, `"`), 64); err == nil {
		v, cerr := convertUnit(f, defaultUnit)
		s := Scalar{Raw: strings.Trim(trimmed, `"`), Unit: defaultUnit, Value: v, Err: errText(cerr)}
		if cerr != nil {
			add(ESemantic, path, cerr.Error())
		}
		return s
	}

	// Quoted scalar, e.g. "12.4us".
	return parseTextQuantity(strings.Trim(trimmed, `"`), defaultUnit, path, add)
}

func parseTextQuantity(s, defaultUnit, path string, add func(code, p, m string)) Scalar {
	i := strings.IndexAny(s, "0123456789.+-eE")
	_ = i
	numEnd := 0
	for numEnd < len(s) {
		c := s[numEnd]
		if (c >= '0' && c <= '9') || c == '.' || c == '+' || c == '-' || c == 'e' || c == 'E' {
			numEnd++
			continue
		}
		break
	}
	if numEnd == 0 {
		add(ESemantic, path, fmt.Sprintf("cannot parse quantity %q", s))
		return Scalar{Raw: s, Unit: defaultUnit, Err: "unparseable"}
	}
	num, unit := s[:numEnd], strings.TrimSpace(s[numEnd:])
	if unit == "" {
		unit = defaultUnit
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		add(ESemantic, path, fmt.Sprintf("cannot parse number in %q: %v", s, err))
		return Scalar{Raw: s, Unit: unit, Err: err.Error()}
	}
	v, cerr := convertUnit(f, unit)
	if cerr != nil {
		add(ESemantic, path, cerr.Error())
	}
	return Scalar{Raw: s, Unit: unit, Value: v, Err: errText(cerr)}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// convertUnit converts time (target: ns), frequency (target: Hz) and voltage
// (target: V) measures.
func convertUnit(v float64, unit string) (float64, error) {
	mult := map[string]float64{
		"ns": 1, "us": 1e3, "µs": 1e3, "ms": 1e6, "s": 1e9,
		"Hz": 1, "kHz": 1e3, "KHz": 1e3, "MHz": 1e6, "GHz": 1e9,
		"V": 1, "mV": 1e-3,
	}
	m, ok := mult[strings.TrimSpace(unit)]
	if !ok {
		return 0, fmt.Errorf("unknown unit %q; value kept at original location", unit)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("non-finite value with unit %q", unit)
	}
	return v * m, nil
}

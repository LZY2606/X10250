// Package capture parses edgebench capture text into immutable model.Capture
// material. It never silently substitutes defaults: unconvertible values stay
// at their source position with a diagnostic.
package capture

import (
	"bufio"
	"strconv"
	"strings"

	"edgebench/internal/model"
)

// Parse turns imported capture text into a Capture. The original text is
// attached verbatim (RawText). Fatal syntax problems return a typed
// format_corrupt error; semantic binding problems return semantic_impossible.
func Parse(name, text string) (*model.Capture, error) {
	c := &model.Capture{
		Name:         name,
		Channels:     map[string]model.ChannelCfg{},
		Buses:        map[string]model.BusDef{},
	}
	c.RawText = text

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if strings.HasPrefix(trim, "@") {
			if err := parseDirective(c, trim, lineNo); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(trim, "EDGE") {
			parseEdge(c, trim, lineNo)
			continue
		}
		return nil, model.NewFormatCorrupt("EB-CAP-UNPARSEABLE-LINE",
			"line is neither a directive (@...), an EDGE nor a comment", lineNo)
	}
	if err := sc.Err(); err != nil {
		return nil, model.NewFormatCorrupt("EB-CAP-READ", err.Error(), lineNo)
	}
	if c.FormatVer == "" {
		return nil, model.NewFormatCorrupt("EB-CAP-NO-FORMAT",
			"missing '# edgebench-capture/1' format header", 0)
	}
	finalize(c)
	if err := validate(c); err != nil {
		return nil, err
	}
	return c, nil
}

func parseDirective(c *model.Capture, line string, lineNo int) error {
	fields := strings.Fields(line)
	switch fields[0] {
	case "@format":
		if len(fields) != 2 {
			return model.NewFormatCorrupt("EB-CAP-FORMAT-ARITY", "@format takes one token", lineNo)
		}
		c.FormatVer = fields[1]
	case "@device":
		if len(fields) < 2 {
			return model.NewFormatCorrupt("EB-CAP-DEVICE-ARITY", "@device needs an id", lineNo)
		}
		c.Device = strings.Join(fields[1:], " ")
	case "@sample":
		if len(fields) != 2 {
			return model.NewFormatCorrupt("EB-CAP-SAMPLE-ARITY", "@sample takes one token", lineNo)
		}
		c.SampleRate = parseRate(fields[1])
		if !c.SampleRate.UnitOK {
			c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
				Severity: model.SevError, Code: "EB-CAP-SAMPLE-UNIT", Line: lineNo,
				Message: "sample rate unit unconvertible; raw representation retained", Raw: line,
			})
		}
	case "@channel":
		return parseChannel(c, fields, line, lineNo)
	case "@bus":
		return parseBus(c, fields, line, lineNo)
	case "@threshold":
		return parseThreshold(c, fields, lineNo)
	default:
		return model.NewFormatCorrupt("EB-CAP-UNKNOWN-DIRECTIVE",
			"unknown directive "+fields[0], lineNo)
	}
	return nil
}

func parseChannel(c *model.Capture, f []string, raw string, lineNo int) error {
	var pin, alias, pol string
	for _, tok := range f[1:] {
		switch {
		case pin == "" && !strings.Contains(tok, "="):
			pin = tok
		case strings.HasPrefix(tok, "alias="):
			alias = strings.TrimPrefix(tok, "alias=")
		case strings.HasPrefix(tok, "polarity="):
			pol = strings.TrimPrefix(tok, "polarity=")
		default:
			return model.NewFormatCorrupt("EB-CAP-CHANNEL-SYNTAX", "bad channel token "+tok, lineNo)
		}
	}
	if pin == "" || alias == "" {
		return model.NewFormatCorrupt("EB-CAP-CHANNEL-SYNTAX",
			"@channel needs <pin> alias=<name>", lineNo)
	}
	if pol != "" && pol != "normal" && pol != "inverted" {
		return model.NewFormatCorrupt("EB-CAP-POLARITY-TOKEN",
			"polarity must be normal|inverted, got "+pol, lineNo)
	}
	if _, exists := c.Channels[pin]; exists {
		return model.NewFormatCorrupt("EB-CAP-CHANNEL-DUP", "duplicate channel "+pin, lineNo)
	}
	c.Channels[pin] = model.ChannelCfg{
		Pin: pin, Alias: alias, Inverted: pol == "inverted",
		RawPolarity: pol, RawToken: raw,
	}
	c.ChannelOrder = append(c.ChannelOrder, pin)
	return nil
}

func parseBus(c *model.Capture, f []string, raw string, lineNo int) error {
	if len(f) < 3 {
		return model.NewFormatCorrupt("EB-CAP-BUS-SYNTAX",
			"@bus <kind> name=<n> <role>=<pin>...", lineNo)
	}
	kind := f[1]
	switch kind {
	case "i2c", "spi", "sync":
	default:
		return model.NewFormatCorrupt("EB-CAP-BUS-KIND", "unknown bus kind "+kind, lineNo)
	}
	b := model.BusDef{Kind: kind, Pins: map[string]string{}, Extra: map[string]string{}, RawLine: raw}
	for _, tok := range f[2:] {
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) != 2 {
			return model.NewFormatCorrupt("EB-CAP-BUS-SYNTAX", "bad bus token "+tok, lineNo)
		}
		if kv[0] == "name" {
			b.Name = kv[1]
			continue
		}
		switch kv[0] {
		case "scl", "sda", "sclk", "mosi", "miso", "cs", "clk", "data", "sync":
			b.Pins[kv[0]] = kv[1]
		case "width", "size":
			b.Extra[kv[0]] = kv[1]
		default:
			return model.NewFormatCorrupt("EB-CAP-BUS-ROLE", "unknown bus role "+kv[0], lineNo)
		}
	}
	if b.Name == "" {
		return model.NewFormatCorrupt("EB-CAP-BUS-NAME", "@bus needs name=", lineNo)
	}
	if _, exists := c.Buses[b.Name]; exists {
		return model.NewFormatCorrupt("EB-CAP-BUS-DUP", "duplicate bus name "+b.Name, lineNo)
	}
	c.Buses[b.Name] = b
	c.BusOrder = append(c.BusOrder, b.Name)
	return nil
}

func parseThreshold(c *model.Capture, f []string, lineNo int) error {
	for _, tok := range f[1:] {
		kv := strings.SplitN(tok, "=", 2)
		if len(kv) != 2 {
			return model.NewFormatCorrupt("EB-CAP-THRESHOLD-SYNTAX", "bad threshold token "+tok, lineNo)
		}
		v, ok := parseTimeNS(kv[1])
		if !ok {
			c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
				Severity: model.SevError, Code: "EB-CAP-THRESHOLD-UNIT", Line: lineNo,
				Message: "threshold unit unconvertible; retained at source, no normalization", Raw: strings.Join(f, " "),
			})
			continue
		}
		switch kv[0] {
		case "gap_ns":
			c.GapNS = &v
		case "stretch_ns":
			c.StretchNS = &v
		default:
			return model.NewFormatCorrupt("EB-CAP-THRESHOLD-NAME", "unknown threshold "+kv[0], lineNo)
		}
	}
	return nil
}

func parseEdge(c *model.Capture, line string, lineNo int) {
	f := strings.Fields(line)
	// EDGE <pin> @<time> -> <0|1>
	if len(f) != 5 || f[3] != "->" || !strings.HasPrefix(f[2], "@") || strings.HasPrefix(f[1], "@") {
		c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
			Severity: model.SevFatal, Code: "EB-CAP-EDGE-SYNTAX", Line: lineNo,
			Message: "expected: EDGE <pin> @<time> -> <0|1>", Raw: line,
		})
		return
	}
	pin := f[1]
	rawT := strings.TrimPrefix(f[2], "@")
	rawLvl := f[4]
	re := model.RawEdge{LineNo: lineNo, Channel: pin, RawT: rawT, RawLVL: rawLvl, TNS: -1, Level: -1}
	t, okT := parseTimeNS(rawT)
	lvl, okL := parseLevel(rawLvl)
	re.OK = okT && okL
	if okT {
		re.TNS = t
	} else {
		c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
			Severity: model.SevError, Code: "EB-CAP-EDGE-TIME-UNIT", Line: lineNo,
			Message: "edge time unit unconvertible; raw token retained, edge excluded from timelines", Raw: line,
		})
	}
	if okL {
		if ch, known := c.Channels[pin]; known && ch.Inverted {
			re.Level = 1 - lvl
		} else {
			re.Level = lvl
		}
	} else {
		c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
			Severity: model.SevError, Code: "EB-CAP-EDGE-LEVEL-TOKEN", Line: lineNo,
			Message: "edge level must be 0 or 1; raw token retained, edge excluded", Raw: line,
		})
	}
	c.RawEdges = append(c.RawEdges, re)
}

func validate(c *model.Capture) error {
	for _, name := range c.BusOrder {
		b := c.Buses[name]
		need := map[string]bool{}
		switch b.Kind {
		case "i2c":
			need = map[string]bool{"scl": true, "sda": true}
		case "spi":
			need = map[string]bool{"sclk": true, "cs": true}
			if _, mos := b.Pins["mosi"]; !mos {
				if _, mis := b.Pins["miso"]; !mis {
					return model.NewSemanticImpossible("EB-CAP-SPI-NO-DATA",
						"spi bus needs mosi and/or miso", lineOf(b.RawLine), name)
				}
			}
		case "sync":
			need = map[string]bool{"clk": true, "data": true, "sync": true}
			if b.Extra["width"] == "" || b.Extra["size"] == "" {
				return model.NewSemanticImpossible("EB-CAP-SYNC-NO-SHAPE",
					"sync bus needs width=<ns> and size=<bits>", lineOf(b.RawLine), name)
			}
			if _, err := strconv.Atoi(b.Extra["width"]); err != nil {
				return model.NewFormatCorrupt("EB-CAP-SYNC-WIDTH-TOKEN",
					"sync width must be integer ns", lineOf(b.RawLine))
			}
			if _, err := strconv.Atoi(b.Extra["size"]); err != nil {
				return model.NewFormatCorrupt("EB-CAP-SYNC-SIZE-TOKEN",
					"sync size must be integer bits", lineOf(b.RawLine))
			}
		}
		for role := range need {
			pin, ok := b.Pins[role]
			if !ok {
				return model.NewSemanticImpossible("EB-CAP-BUS-MISSING-ROLE",
					"bus "+name+" missing role "+role, lineOf(b.RawLine), name)
			}
			if _, ok := c.Channels[pin]; !ok {
				return model.NewSemanticImpossible("EB-CAP-BUS-UNKNOWN-PIN",
					"role "+role+" references undeclared channel "+pin, lineOf(b.RawLine), name)
			}
		}
		for role, pin := range b.Pins {
			if _, ok := c.Channels[pin]; !ok {
				return model.NewSemanticImpossible("EB-CAP-BUS-UNKNOWN-PIN",
					"role "+role+" references undeclared channel "+pin, lineOf(b.RawLine), name)
			}
		}
	}
	seen := map[string]bool{}
	for _, re := range c.RawEdges {
		if _, known := c.Channels[re.Channel]; !known {
			return model.NewSemanticImpossible("EB-CAP-EDGE-UNKNOWN-CHANNEL",
				"edge on undeclared channel "+re.Channel, re.LineNo, "")
		}
		if re.OK {
			key := re.Channel + "@" + strconv.FormatInt(re.TNS, 10)
			if seen[key] {
				c.ImportDiagnostics = append(c.ImportDiagnostics, model.Diagnostic{
					Severity: model.SevWarning, Code: "EB-CAP-EDGE-DUP", Line: re.LineNo,
					Message: "duplicate edge at same pin/time; first occurrence retained", Raw: "",
				})
			}
			seen[key] = true
		}
	}
	return nil
}

func lineOf(raw string) int {
	// Line numbers for bus errors are attached by caller where useful; RawLine
	// is retained on the BusDef for provenance.
	return 0
}

// finalize keeps RawEdges in source order: normalized, deduplicated edges are
// derived by decode.Normalize, never mutating material.
func finalize(_ *model.Capture) {}

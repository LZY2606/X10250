package capture

import (
	"fmt"
	"strings"

	"edgetrace/internal/model"
)

func (p *parser) parseBodyLine(lt lineTok) error {
	t := lt.text
	switch {
	case strings.HasPrefix(t, "sample_rate "):
		p.parseRateLine(lt)
	case strings.HasPrefix(t, "decoder "):
		p.parseDecoderLine(lt)
	case strings.HasPrefix(t, "channel "):
		p.parseChannelLine(lt)
	case strings.HasPrefix(t, "bus "):
		p.parseBusLine(lt)
	case strings.HasPrefix(t, "polarity "):
		p.parsePolarityLine(lt)
	case strings.HasPrefix(t, "edge "):
		p.parseEdgeLine(lt)
	default:
		return CorruptInputError("unrecognized statement",
			diag("E_SYNTAX", "error", lt.line, 1, "unrecognized statement: "+firstWord(t)))
	}
	return nil
}

func (p *parser) parseChannelLine(lt lineTok) {
	m := channelRE.FindStringSubmatch(lt.text)
	if m == nil {
		p.diags = append(p.diags, diag("E_CHANNEL_SYNTAX", "error", lt.line, 1,
			"malformed channel statement"))
		return
	}
	name, alias, invRaw := m[1], m[2], m[3]
	if alias == "" {
		alias = name
	}
	inverted := false
	invText := "false"
	if invRaw != "" {
		invText = invRaw
		inverted = invRaw == "inverted" || strings.Contains(invRaw, "true") || strings.HasSuffix(invRaw, "=1")
	}
	if _, dup := p.chIndex[name]; dup {
		p.diags = append(p.diags, diag("W_CHANNEL_DUP", "warn", lt.line, 1,
			"duplicate channel "+name+"; first definition retained"))
		return
	}
	p.chIndex[name] = len(p.channels)
	p.channels = append(p.channels, model.Channel{
		Name: name, Alias: alias, Inverted: inverted, InvertText: invText,
	})
	col := len("channel ") + 1
	p.refs = append(p.refs, model.SourceRef{Kind: "alias", Line: lt.line, Column: col, Channel: name})
	p.refs = append(p.refs, model.SourceRef{Kind: "invert", Line: lt.line, Column: col, Channel: name})
}

func (p *parser) parseBusLine(lt lineTok) {
	m := busRE.FindStringSubmatch(lt.text)
	if m == nil {
		p.diags = append(p.diags, diag("E_BUS_SYNTAX", "error", lt.line, 1,
			"malformed bus statement (want: bus <kind> <name> roles k=v ...)"))
		return
	}
	kind, name, rolesText := m[1], m[2], m[3]
	switch kind {
	case model.BusI2C, model.BusSPI, model.BusCustom:
	default:
		p.diags = append(p.diags, diag("E_BUS_KIND", "error", lt.line, 1,
			"unknown bus kind "+kind))
		return
	}
	roles := map[string]string{}
	for _, pair := range strings.Fields(rolesText) {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			p.diags = append(p.diags, diag("E_ROLE_SYNTAX", "error", lt.line, 1,
				"malformed role "+pair))
			continue
		}
		roles[kv[0]] = kv[1]
	}
	p.buses = append(p.buses, model.Bus{Kind: kind, Name: name, Roles: roles})
}

func (p *parser) parsePolarityLine(lt lineTok) {
	m := polRE.FindStringSubmatch(lt.text)
	if m == nil {
		p.diags = append(p.diags, diag("E_POLARITY_SYNTAX", "error", lt.line, 1,
			"malformed polarity statement"))
		return
	}
	bus, pol := m[1], m[2]
	p.polarity[bus] = pol
	p.polText[bus] = pol
	p.refs = append(p.refs, model.SourceRef{Kind: "polarity", Line: lt.line, Column: len("polarity ") + 1, Channel: bus})
}

func (p *parser) parseEdgeLine(lt lineTok) {
	var ch, timeText, sampleText, lvlText, idText string
	var hasTime bool
	if m := edgeRE2.FindStringSubmatch(lt.text); m != nil {
		ch, timeText, sampleText, lvlText, idText = m[1], m[2], m[3], m[4], m[5]
		hasTime = true
	} else if m := edgeRE1.FindStringSubmatch(lt.text); m != nil {
		ch, sampleText, lvlText, idText = m[1], m[2], m[3], m[4]
	} else {
		p.diags = append(p.diags, diag("E_EDGE_SYNTAX", "error", lt.line, 1,
			"malformed edge statement"))
		return
	}
	col := len("edge ") + 1
	if _, ok := p.chIndex[ch]; !ok {
		d := diag("E_EDGE_CHANNEL", "error", lt.line, col,
			"edge references undeclared channel "+ch)
		d.Channel = ch
		d.RawValue = lt.text
		p.diags = append(p.diags, d)
		return
	}
	sample, ok := atoiSafe(sampleText)
	if !ok || sample < 0 {
		d := diag("E_EDGE_SAMPLE", "error", lt.line, col,
			"invalid sample index "+sampleText)
		d.Channel = ch
		d.RawValue = sampleText
		p.diags = append(p.diags, d)
		return
	}
	lvlRaw, rawText := levelOf(lvlText)

	var off *model.Rat
	offsetText := ""
	if hasTime {
		offsetText = timeText
		r, err := parseTime(timeText)
		if err != nil {
			d := diag("E_EDGE_TIME", "error", lt.line, col, err.Error())
			d.Channel = ch
			d.RawValue = timeText
			p.diags = append(p.diags, d)
			return
		}
		off = &r
	}
	if p.rateSet {
		fromSample := model.MustRat(sample*1_000_000_000, p.rateHz)
		if off == nil {
			off = &fromSample
			offsetText = fmt.Sprintf("sample=%d", sample)
		} else if off.Cmp(fromSample) != 0 {
			d := diag("W_TIME_SAMPLE_MISMATCH", "warn", lt.line, col,
				fmt.Sprintf("time %s disagrees with sample %d at %s; original time retained",
					timeText, sample, p.rateText))
			d.Channel = ch
			d.RawValue = timeText
			p.diags = append(p.diags, d)
		}
	} else if off == nil {
		d := diag("E_EDGE_NO_TIMEBASE", "error", lt.line, col,
			"edge has no time expression and no sample rate is defined")
		d.Channel = ch
		p.diags = append(p.diags, d)
		return
	}

	id := idText
	if id == "" {
		id = stableID("e", fmt.Sprintf("%s|%d|%s", ch, sample, rawText))
	}
	if p.seenEdge[id] {
		p.diags = append(p.diags, diag("W_EDGE_DUP", "warn", lt.line, col,
			"duplicate edge id "+id+" retained once"))
		return
	}
	p.seenEdge[id] = true
	chDef := p.channels[p.chIndex[ch]]
	level := lvlRaw
	if chDef.Inverted {
		level = 1 - level
	}
	p.edges = append(p.edges, model.Edge{
		ID: id, Channel: ch, LogicalCh: chDef.Alias, Sample: sample,
		OffsetNs: off, OffsetText: offsetText, Level: level, LevelRaw: lvlRaw,
	})
	p.refs = append(p.refs, model.SourceRef{Kind: "edge", Line: lt.line, Column: col, EdgeID: id, Channel: ch})
}

func (p *parser) validateBuses() {
	known := map[string]bool{}
	for _, c := range p.channels {
		known[c.Name] = true
	}
	required := map[string][]string{
		model.BusI2C:    {"scl", "sda"},
		model.BusSPI:    {"clk", "mosi"},
		model.BusCustom: {"clk", "data"},
	}
	for _, b := range p.buses {
		for _, role := range required[b.Kind] {
			ch, ok := b.Roles[role]
			if !ok {
				p.diags = append(p.diags, diag("E_BUS_ROLE", "error", 0, 0,
					"bus "+b.Name+" missing role "+role))
				continue
			}
			if !known[ch] {
				p.diags = append(p.diags, diag("E_BUS_CHANNEL", "error", 0, 0,
					"bus "+b.Name+" role "+role+" maps to undeclared channel "+ch))
			}
		}
	}
}

// Package ingest parses the domain-specific .ebc edge capture format into
// immutable model.Capture material. Nothing here normalizes away raw
// evidence: original edge time text, sample rate text, channel aliases,
// inversion flags, declared clock polarity and decoder version are all
// retained.
package ingest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"edgebench/internal/model"
	"edgebench/internal/qty"
)

const (
	FormatName    = "edgebench-capture"
	FormatVersion = "1.0"
	DecoderID     = "edgebench-decoder"
	DecoderVer    = "2.4.1"
)

// Parse converts source text into a Capture. Structural/lexical problems
// come back as model.StatusCorrupt diagnostics with line/column positions;
// well-formed but contradictory declarations (e.g. sample-count times with
// no rate) are StatusSemanticImpasse. Parse never silently substitutes
// defaults for invalid values.
func Parse(src string) (model.Capture, []model.Diag) {
	cap0 := model.Capture{
		Format: FormatName, FormatVer: FormatVersion,
		ImportedAt: time.Now().UTC(),
		Headers: map[string]string{},
	}
	diags := []model.Diag{}
	add := func(d ...model.Diag) { diags = append(diags, d...) }

	lines := splitLines(src)
	cap0.Raw = src
	seenMagic := false
	rateHz := 0.0
	chByName := map[string]*model.Channel{}
	edgeSeq := 0
	edgeCount := map[string]int{}

	for li, rawLine := range lines {
		lineNo := li + 1
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !seenMagic {
			if line != "edgebench-capture/1.0" {
				return cap0, []model.Diag{{Code: "bad_magic", Severity: model.SevError, Line: lineNo, Col: 1,
					Message: fmt.Sprintf("first non-empty line must be %q", "edgebench-capture/1.0"), Kind: model.StatusCorrupt}}
			}
			seenMagic = true
			continue
		}
		if strings.HasPrefix(line, "edges:") {
			break
		}
		key, val, ok := splitKV(line, lineNo)
		if !ok {
			add(model.Diag{Code: "bad_header", Severity: model.SevError, Line: lineNo,
				Message: fmt.Sprintf("expected key: value, got %q", truncate(line, 40)), Kind: model.StatusCorrupt})
			continue
		}
		cap0.Headers[key] = val
		col := len(key) + 2
		switch key {
		case "device":
			if strings.TrimSpace(val) == "" {
				add(model.Diag{Code: "device_empty", Severity: model.SevError, Line: lineNo, Col: col,
					Message: "device is empty", Kind: model.StatusCorrupt})
			}
			cap0.Device = strings.TrimSpace(val)
		case "rate":
			q, hz, ds := qty.ParseRate(val, lineNo)
			add(ds...)
			cap0.SampleRate = q
			rateHz = hz
		case "i2c", "spi", "sync":
			chs, ds := parseChannels(key, val, lineNo, col)
			add(ds...)
			for i := range chs {
				c := chs[i]
				if _, dup := chByName[c.Name]; dup {
					add(model.Diag{Code: "channel_duplicate", Severity: model.SevError, Line: lineNo, Col: col,
						Path: "channel:" + c.Name, Message: fmt.Sprintf("duplicate channel %q", c.Name), Kind: model.StatusCorrupt})
				}
				chByName[c.Name] = &c
				cap0.Channels = append(cap0.Channels, c)
			}
		default:
			add(model.Diag{Code: "unknown_header", Severity: model.SevWarning, Line: lineNo, Col: col,
				Message: fmt.Sprintf("unknown header %q retained verbatim", key)})
		}
	}

	if cap0.Device == "" {
		add(model.Diag{Code: "missing_device", Severity: model.SevError, Path: "device",
			Message: "capture declares no device", Kind: model.StatusCorrupt})
	}
	if !cap0.SampleRate.OK {
		if cap0.SampleRate.Raw == "" {
			add(model.Diag{Code: "missing_rate", Severity: model.SevError, Path: "rate",
				Message: "capture declares no sample rate", Kind: model.StatusCorrupt})
		}
	}
	if len(cap0.Channels) == 0 {
		add(model.Diag{Code: "no_channels", Severity: model.SevError, Path: "channels",
			Message: "capture declares no channels", Kind: model.StatusCorrupt})
	}

	// edges: section
	inEdges := false
	for li, rawLine := range lines {
		lineNo := li + 1
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "edges:") {
			inEdges = true
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "edges:"))
			if rest != "" {
				add(model.Diag{Code: "edges_trailing", Severity: model.SevError, Line: lineNo,
					Message: "edges: header must have no trailing tokens", Kind: model.StatusCorrupt})
			}
			continue
		}
		if !inEdges {
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 3 {
			add(model.Diag{Code: "edge_arity", Severity: model.SevError, Line: lineNo,
				Message: fmt.Sprintf("edge needs exactly 3 tokens <ch> <time> <0|1>, got %d", len(fields)), Kind: model.StatusCorrupt})
			continue
		}
		ch := fields[0]
		chDef, known := chByName[ch]
		if !known {
			add(model.Diag{Code: "edge_unknown_channel", Severity: model.SevError, Line: lineNo, Col: 1,
				Path: "edge:" + ch, Message: fmt.Sprintf("edge on undeclared channel %q", ch), Kind: model.StatusSemanticImpasse})
			continue
		}
		tq, ds := qty.ParseTime(fields[1], lineNo, rateHz)
		add(ds...)
		lvl, err := strconv.Atoi(fields[2])
		if err != nil || (lvl != 0 && lvl != 1) {
			add(model.Diag{Code: "edge_level", Severity: model.SevError, Line: lineNo, Col: len(fields[0]) + len(fields[1]) + 3,
				Message: fmt.Sprintf("edge level must be 0 or 1, got %q", fields[2]), Kind: model.StatusCorrupt})
			continue
		}
		if !tq.OK {
			// Preserve the edge at its original position; decoders must not
			// treat unconvertible times as usable evidence.
			cap0.Edges = append(cap0.Edges, model.Edge{Seq: edgeSeq, Ch: ch, Time: tq, Level: lvl, Line: lineNo})
			edgeSeq++
			edgeCount[ch]++
			continue
		}
		_ = chDef
		cap0.Edges = append(cap0.Edges, model.Edge{Seq: edgeSeq, Ch: ch, Time: tq, Level: lvl, Line: lineNo})
		edgeSeq++
		edgeCount[ch]++
	}
	if inEdges == false {
		add(model.Diag{Code: "no_edges_section", Severity: model.SevError, Path: "edges",
			Message: "capture has no edges: section", Kind: model.StatusCorrupt})
	}

	// Ordering/monotonicity checks per channel (raw evidence retained,
	// reported with position).
	lastForCh := map[string]model.Edge{}
	for _, e := range cap0.Edges {
		if e.Time.OK {
			if prev, ok := lastForCh[e.Ch]; ok && e.Time.Normalized < prev.Time.Normalized {
				add(model.Diag{Code: "edge_time_regress", Severity: model.SevError, Line: e.Line,
					Path: fmt.Sprintf("edge:%s#%d", e.Ch, e.Seq),
					Message: fmt.Sprintf("time %s precedes earlier edge on %s", e.Time.Raw, e.Ch), Kind: model.StatusSemanticImpasse})
			}
			lastForCh[e.Ch] = e
		}
	}

	cap0.ID = model.HashID("cap", src)
	cap0.Diags = diags
	return cap0, diags
}

func splitKV(line string, lineNo int) (string, string, bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:i])
	val := strings.TrimSpace(line[i+1:])
	if strings.ContainsAny(key, " \t") {
		return "", "", false
	}
	return key, val, true
}

// parseChannels parses e.g.
//
//	i2c: sda=SDA inv=sda scl=SCL pol=idle_high
//	spi: clk=SCK pol=low pha=leading mosi=MOSI cs=CS0,CS1 cs_active=low
//	sync: clk=DCLK io=DIO
func parseChannels(bus, val string, lineNo, col int) ([]model.Channel, []model.Diag) {
	var diags []model.Diag
	tokens := strings.Fields(val)
	named := map[string]string{}
	var order []string
	invert := map[string]bool{}
	pol := ""
	csActive := ""
	for _, tk := range tokens {
		eq := strings.IndexByte(tk, '=')
		if eq <= 0 {
			diags = append(diags, model.Diag{Code: "ch_bad_token", Severity: model.SevError, Line: lineNo,
				Message: fmt.Sprintf("channel token must be key=value: %q", tk), Kind: model.StatusCorrupt})
			continue
		}
		k, v := tk[:eq], tk[eq+1:]
		switch k {
		case "inv":
			invert[v] = true
		case "pol":
			pol = v
		case "cs_active":
			csActive = v
		default:
			if _, exists := named[k]; exists {
				diags = append(diags, model.Diag{Code: "ch_dup_role", Severity: model.SevError, Line: lineNo,
					Message: fmt.Sprintf("role %q declared twice", k), Kind: model.StatusCorrupt})
			}
			named[k] = v
			order = append(order, k)
		}
	}
	var required []string
	switch bus {
	case "i2c":
		required = []string{"sda", "scl"}
	case "spi":
		required = []string{"clk", "mosi"}
	case "sync":
		required = []string{"clk", "io"}
	}
	for _, r := range required {
		if _, ok := named[r]; !ok {
			diags = append(diags, model.Diag{Code: "ch_missing_role", Severity: model.SevError, Line: lineNo, Col: col,
				Path: fmt.Sprintf("%s.%s", bus, r),
				Message: fmt.Sprintf("%s capture must declare %s channel", bus, r), Kind: model.StatusSemanticImpasse})
		}
	}
	if pol != "" {
		allowed := map[string]string{}
		switch bus {
		case "i2c":
			allowed = map[string]string{"idle_high": "idle_high", "inverted": "inverted"}
		case "spi":
			allowed = map[string]string{"low": "low", "high": "high"}
		}
		if _, ok := allowed[strings.ToLower(pol)]; !ok && bus != "sync" {
			diags = append(diags, model.Diag{Code: "bad_polarity", Severity: model.SevError, Line: lineNo,
				Path: bus + ".pol",
				Message: fmt.Sprintf("invalid clock polarity %q for %s", pol, bus), Kind: model.StatusCorrupt})
		}
	}
	if csActive != "" && csActive != "low" && csActive != "high" {
		diags = append(diags, model.Diag{Code: "bad_cs_active", Severity: model.SevError, Line: lineNo,
			Message: fmt.Sprintf("cs_active must be low|high, got %q", csActive), Kind: model.StatusCorrupt})
	}
	var out []model.Channel
	seen := map[string]bool{}
	emit := func(role, name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, model.Channel{
			Name: name, Alias: role, Inverted: invert[name],
			Role: bus + ":" + role, Initial: -1,
		})
	}
	for _, role := range order {
		val := named[role]
		if role == "cs" {
			for _, name := range strings.Split(val, ",") {
				name = strings.TrimSpace(name)
				if name == "" {
					continue
				}
				if seen[name] {
					diags = append(diags, model.Diag{Code: "channel_duplicate", Severity: model.SevError, Line: lineNo,
						Path: "channel:" + name, Message: fmt.Sprintf("duplicate channel %q", name), Kind: model.StatusCorrupt})
					continue
				}
				seen[name] = true
				out = append(out, model.Channel{Name: name, Alias: "cs", Inverted: invert[name],
					Role: bus + ":cs", Initial: -1})
			}
			continue
		}
		emit(role, val)
	}
	return out, diags
}

func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Split(s, "\n")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

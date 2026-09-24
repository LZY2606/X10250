// Package ingest parses the offline edge/probe text formats. Parsing never
// fabricates data: unparseable quantities stay at their source position with a
// diagnostic, and callers distinguish parse corruption from semantic gaps.
package ingest

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"edgebench/internal/model"
	"edgebench/internal/quant"
)

// Fatal marks corruption severe enough that no structured capture exists.
type Fatal struct {
	Code string
	quant.Diagnostic
}

func (f Fatal) Error() string { return f.Code + ": " + f.Diagnostic.Error() }

// Header carries values retained verbatim from the file preamble.
type Header struct {
	Kind       model.Kind
	SampleRate quant.Q
	Diags      []quant.Diagnostic
}

// ParseEdges parses:
//
//	# edgebench/edges v1
//	# kind i2c
//	# sample-rate 12.5MHz
//	<time> <channel> <0|1>
//
// Comments and blank lines are ignored; every parsed edge retains its raw line.
func ParseEdges(filename string, data []byte) (model.Capture, []model.Edge, error) {
	cap_ := model.Capture{Name: filename}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), 1<<22)
	line := 0
	var edges []model.Edge
	seen := map[string]bool{}
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			if d := parseHeader(text, &cap_); d != nil {
				cap_.Diags = append(cap_.Diags, *d)
			}
			continue
		}
		pos := quant.Pos{File: filename, Line: line, Col: 1}
		fields := strings.Fields(text)
		if len(fields) < 3 {
			cap_.Diags = append(cap_.Diags, quant.Diagnostic{Code: "EDGE_FIELDS", Message: fmt.Sprintf("want <time> <channel> <level>, got %q", text), Pos: pos})
			continue
		}
		tq, d := quant.Parse(fields[0], pos, "s")
		if d != nil {
			cap_.Diags = append(cap_.Diags, *d)
			continue
		}
		ch := fields[1]
		lvl, err := strconv.Atoi(fields[2])
		if err != nil || (lvl != 0 && lvl != 1) {
			cap_.Diags = append(cap_.Diags, quant.Diagnostic{Code: "EDGE_LEVEL", Message: fmt.Sprintf("level must be 0 or 1, got %q", fields[2]), Pos: pos})
			continue
		}
		key := fields[0] + "|" + ch
		if seen[key] {
			cap_.Diags = append(cap_.Diags, quant.Diagnostic{Code: "EDGE_DUP", Message: fmt.Sprintf("duplicate edge for %s at %s", ch, fields[0]), Pos: pos})
		}
		seen[key] = true
		id := HashID("edge", filename, strconv.Itoa(line), fields[0], ch, strconv.Itoa(lvl))
		edges = append(edges, model.Edge{
			ID: id, Channel: ch, T: tq.Value, TimeRaw: tq, Level: lvl,
			RawLevel: lvl, Source: pos, LineText: text,
		})
	}
	if err := sc.Err(); err != nil {
		return cap_, nil, Fatal{Code: "FORMAT_CORRUPT", Diagnostic: quant.Diagnostic{Message: "scanner failure: " + err.Error()}}
	}
	if cap_.Kind == "" {
		return cap_, nil, Fatal{Code: "FORMAT_CORRUPT", Diagnostic: quant.Diagnostic{
			Code: "HEADER_KIND", Message: "missing '# kind <i2c|spi|sync>' header", Pos: quant.Pos{File: filename},
		}}
	}
	cap_.Edges = edges
	return cap_, edges, nil
}

func parseHeader(text string, cap_ *model.Capture) *quant.Diagnostic {
	if !strings.HasPrefix(text, "#") {
		return nil
	}
	f := strings.Fields(text)
	if len(f) < 2 {
		return nil
	}
	pos := quant.Pos{File: cap_.Name, Line: 0}
	switch f[1] {
	case "kind":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "HEADER_KIND", Message: "kind needs a value", Pos: pos}
		}
		switch model.Kind(f[2]) {
		case model.KindI2C, model.KindSPI, model.KindSync:
			cap_.Kind = model.Kind(f[2])
		default:
			return &quant.Diagnostic{Code: "HEADER_KIND", Message: "unknown kind " + f[2], Pos: pos}
		}
	case "sample-rate":
		if len(f) < 3 {
			return &quant.Diagnostic{Code: "HEADER_RATE", Message: "sample-rate needs a value", Pos: pos}
		}
		q, d := quant.Parse(f[2], pos, "Hz")
		if d != nil {
			cp := *d
			return &cp
		}
		cap_.SampleRate = q
	}
	return nil
}

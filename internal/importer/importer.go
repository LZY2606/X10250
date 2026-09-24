// Package importer reads the domain-specific ".logic" edge capture format.
// The format is intentionally NOT a generic table: sections are
// HEADER / CHANNELS / PROBE / EDGES with typed directives and units.
package importer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"logicbench/internal/model"
)

const FormatVersion = "logic/1"

// RawDocument is the parsed-but-normalized document. Diagnostics collect
// recoverable problems (bad units, unknown roles). A corrupt file yields a
// model.NewCorrupt typed error instead.
type RawDocument struct {
	FileName   string
	FormatVer  string
	Header     map[string]string
	Channels   []RawChannel
	Probe      RawProbe
	Edges      []RawEdge
	Diagnostics []model.Diagnostic
}

type RawChannel struct {
	Line     int
	Index    int
	Alias    string
	Physical string
	Inverted bool
}

type RawProbe struct {
	Line       int
	Bus        string
	Decoder    string
	DecVer     string
	SampleRate string
	Roles      map[string]string
	CPOL       *bool
	CPHA       *bool
	Gap        string
	Stretch    string
	SyncWord   string
	Note       string
}

type RawEdge struct {
	Line    int
	Channel string
	TimeRaw string
	LevelRaw string
}

// Budget constrains the importer; exceeding it is a distinct terminal result.
type Budget struct {
	MaxEdges      int
	MaxChannels   int
	MaxLineLength int
}

func DefaultBudget() Budget { return Budget{MaxEdges: 200000, MaxChannels: 64, MaxLineLength: 4096} }

func diag(projectID, importID, code, sev, msg string, line int) model.Diagnostic {
	d := model.Diagnostic{
		ProjectID: projectID, ImportID: importID, Code: code, Severity: sev,
		Message: msg, At: time.Now().UTC(),
	}
	if line > 0 {
		d.Position = &model.SourcePosition{Line: line}
	}
	return d
}

// Parse performs a positioned parse. raw is the exact bytes; its sha is the
// canonical identity of the source material.
func Parse(fileName string, raw []byte, b Budget) (*RawDocument, string, error) {
	sum := sha256.Sum256(raw)
	sha := hex.EncodeToString(sum[:])
	doc := &RawDocument{
		FileName: fileName, FormatVer: FormatVersion,
		Header: map[string]string{}, Probe: RawProbe{Roles: map[string]string{}},
	}
	section := ""
	edgeCount := 0
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, b.MaxLineLength), b.MaxLineLength)
	lineNo := 0
	seenEdge := false
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if len(line) > b.MaxLineLength {
			return nil, sha, model.NewCorrupt("line_too_long",
				fmt.Sprintf("line %d exceeds %d bytes", lineNo, b.MaxLineLength),
				&model.SourcePosition{File: fileName, Line: lineNo})
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		col := strings.Index(line, trimmed) + 1
		pos := func() *model.SourcePosition {
			return &model.SourcePosition{File: fileName, Line: lineNo, Column: col}
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]"))
			switch section {
			case "header", "channels", "probe", "edges":
			default:
				return nil, sha, model.NewCorrupt("unknown_section",
					"unknown section ["+section+"]", pos())
			}
			continue
		}
		key, val, hasEq := strings.Cut(trimmed, "=")
		if !hasEq {
			// in EDGES a bare "alias time level" triple is valid; else corrupt.
			if section == "edges" {
				fields := strings.Fields(trimmed)
				if len(fields) != 3 {
					return nil, sha, model.NewCorrupt("edge_fields",
						"edge must be 'alias time level'", pos())
				}
				edgeCount++
				if edgeCount > b.MaxEdges {
					return nil, sha, &model.TypedError{
						Class: model.ResultBudgetExhausted, Code: "edge_budget",
						Message: fmt.Sprintf("more than %d edges", b.MaxEdges),
						Pos:     pos(),
					}
				}
				doc.Edges = append(doc.Edges, RawEdge{Line: lineNo, Channel: fields[0], TimeRaw: fields[1], LevelRaw: fields[2]})
				seenEdge = true
				continue
			}
			return nil, sha, model.NewCorrupt("expected_assignment",
				"expected key = value", pos())
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch section {
		case "header":
			doc.Header[strings.ToLower(key)] = val
		case "channels":
			if len(doc.Channels) >= b.MaxChannels {
				return nil, sha, &model.TypedError{Class: model.ResultBudgetExhausted,
					Code: "channel_budget", Message: "too many channels", Pos: pos()}
			}
			ch, err := parseChannel(lineNo, key, val)
			if err != nil {
				return nil, sha, err
			}
			doc.Channels = append(doc.Channels, ch)
		case "probe":
			if err := doc.Probe.apply(lineNo, key, val); err != nil {
				return nil, sha, err
			}
		case "edges":
			return nil, sha, model.NewCorrupt("edge_assignment",
				"edges section only accepts 'alias time level' rows", pos())
		default:
			return nil, sha, model.NewCorrupt("outside_section",
				"directive outside any section", pos())
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, sha, model.NewCorrupt("scan", err.Error(), nil)
	}
	if fv := doc.Header["format"]; fv != "" {
		doc.FormatVer = fv
	}
	if doc.Probe.Bus == "" {
		return nil, sha, model.NewCorrupt("missing_bus", "[probe] bus is required", nil)
	}
	if !seenEdge {
		return nil, sha, model.NewCorrupt("no_edges", "[edges] contains no rows", nil)
	}
	return doc, sha, nil
}

func parseChannel(line int, key, val string) (RawChannel, error) {
	idx, err := strconv.Atoi(key)
	if err != nil {
		return RawChannel{}, model.NewCorrupt("channel_index",
			"channel key must be an integer: "+key,
			&model.SourcePosition{Line: line})
	}
	ch := RawChannel{Line: line, Index: idx, Alias: val}
	return ch, nil
}

func (p *RawProbe) apply(line int, key, val string) error {
	pos := &model.SourcePosition{Line: line}
	corrupt := func(code, msg string) error { return model.NewCorrupt(code, msg, pos) }
	switch strings.ToLower(key) {
	case "bus":
		switch val {
		case "i2c", "spi", "sync":
			p.Bus = val
		default:
			return corrupt("bad_bus", "bus must be i2c|spi|sync")
		}
	case "decoder":
		p.Decoder = val
	case "decoder_version", "version":
		p.DecVer = val
	case "sample_rate", "samplerate":
		p.SampleRate = val
	case "cpol":
		switch val {
		case "0":
			p.CPOL = model.BoolPtr(false)
		case "1":
			p.CPOL = model.BoolPtr(true)
		case "?", "unknown":
			p.CPOL = nil
		default:
			return corrupt("bad_cpol", "cpol must be 0|1|?")
		}
	case "cpha":
		switch val {
		case "0":
			p.CPHA = model.BoolPtr(false)
		case "1":
			p.CPHA = model.BoolPtr(true)
		case "?", "unknown":
			p.CPHA = nil
		default:
			return corrupt("bad_cpha", "cpha must be 0|1|?")
		}
	case "gap_threshold", "gap":
		p.Gap = val
	case "stretch_tol", "stretch":
		p.Stretch = val
	case "sync_word", "sync":
		p.SyncWord = val
	case "note":
		p.Note = val
	default:
		if strings.HasPrefix(strings.ToLower(key), "role.") {
			role := strings.TrimPrefix(strings.ToLower(key), "role.")
			p.Roles[role] = val
			return nil
		}
		return corrupt("unknown_probe_key", "unknown probe key: "+key)
	}
	return nil
}

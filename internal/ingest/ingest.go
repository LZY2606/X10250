// Package ingest loads the domain-specific offline case pack
// ("边沿译码台": logic analyzer waves, I2C/SPI transactions and overlapping
// candidates). It is deliberately not a generic spreadsheet: the container
// understands captures, probe configuration, edge runs and a replay script.
package ingest

import (
	"bytes"
	"encoding/json"
	"fmt"

	"edgebench/internal/decode"
)

const PackFormat = "edgebench.casepack/1"

// Pack is the raw, on-disk representation. Strings are intentionally kept as
// the original representation ("10 MHz", "12.5 us", "s312") so the source
// layer preserves units and coordinates; normalization happens only in the
// derived layer.
type Pack struct {
	Format   string         `json:"format"`
	Title    string         `json:"title"`
	Project  string         `json:"project_id"`
	MaxEdges int            `json:"max_edges"`
	Captures []PackCapture  `json:"captures"`
	Script   []PackScriptOp `json:"script"`
}

type PackCapture struct {
	ID       string         `json:"id"`
	Protocol string         `json:"protocol"`
	Decoder  string         `json:"decoder_version"`
	Params   PackParams     `json:"params"`
	Channels []PackChannel  `json:"channels"`
	Edges    []PackEdgeLine `json:"edges"`
}

type PackParams struct {
	SampleRate     string `json:"sample_rate"`
	TimeBase       string `json:"time_base"`
	CoordinateBase string `json:"coordinate_base"`
	ClockPolarity  string `json:"clock_polarity"`
	ClockPhase     string `json:"clock_phase"`
	SpiMode        string `json:"spi_mode"`
	BitOrder       string `json:"bit_order"`
	StretchNS      int64  `json:"stretch_ns"`
	GapNS          int64  `json:"gap_ns"`
	ActiveLevel    string `json:"active_level"`
	SyncWord       int    `json:"sync_word"`
	SyncBits       int    `json:"sync_bits"`
}

type PackChannel struct {
	ID         string `json:"id"`
	Alias      string `json:"alias"`
	Probe      string `json:"probe"`
	Role       string `json:"role"`
	Inverted   bool   `json:"inverted"`
	InitialRaw *int   `json:"initial_raw"`
	Threshold  string `json:"threshold"`
}

// PackEdgeLine keeps the exact source text and an optional origin locator.
type PackEdgeLine struct {
	Channel string `json:"ch"`
	Time    string `json:"t"`
	Level   int    `json:"l"`
	Origin  string `json:"origin"`
}

type PackScriptOp struct {
	Op        string         `json:"op"`
	Capture   string         `json:"capture"`
	ID        string         `json:"id"`
	Reason    string         `json:"reason"`
	Params    *PackParams    `json:"params"`
	Edges     []PackEdgeLine `json:"edges"`
	Mode      string         `json:"mode"`
	Action    string         `json:"action"`
	TxnIndex  *int           `json:"txn_index"`
	CandIndex *int           `json:"cand_index"`
	TxnID     string         `json:"txn_id"`
	CandID    string         `json:"candidate_id"`
}

// Load parses a case pack with position-bearing diagnostics.
func Load(data []byte) (*Pack, error) {
	if len(data) == 0 {
		return nil, &decode.CorruptError{Path: "pack", Pos: 0, Message: "empty case pack"}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var p Pack
	if err := dec.Decode(&p); err != nil {
		if se, ok := err.(*json.SyntaxError); ok {
			line, col := lineCol(data, se.Offset)
			return nil, &decode.CorruptError{Path: "pack", Pos: int(se.Offset), Line: line, Column: col, Message: se.Error()}
		}
		return nil, &decode.CorruptError{Path: "pack", Message: err.Error()}
	}
	if p.Format != PackFormat {
		return nil, &decode.CorruptError{Path: "format", Message: "unsupported pack format: " + p.Format}
	}
	return &p, nil
}

func lineCol(data []byte, off int64) (int, int) {
	line, col := 1, 1
	for i := int64(0); i < off && i < int64(len(data)); i++ {
		if data[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

// Build turns the raw pack into immutable capture revisions (revision 0 for
// each capture). Unconvertible values stay in place with diagnostics.
func Build(p *Pack) (string, []*decode.Capture, []decode.Diagnostic, error) {
	var diags []decode.Diagnostic
	if len(p.Captures) == 0 {
		return "", nil, nil, &decode.SemanticError{Diagnostics: []decode.Diagnostic{{
			Code: "no_captures", Path: "captures", Severity: "error",
			Message: "case pack contains no captures",
		}}}
	}
	projectID := p.Project
	if projectID == "" {
		projectID = "prj_" + decode.CanonicalHash("", p.Title+fmt.Sprint(len(p.Captures)))[4:14]
	}
	var caps []*decode.Capture
	totalEdges := 0
	for ci := range p.Captures {
		pc := &p.Captures[ci]
		ver := pc.Decoder
		if ver == "" {
			ver = decode.DecoderVersion
		}
		params, pdiags := buildParams(pc, ci)
		diags = append(diags, pdiags...)
		chans := make([]decode.ChannelSpec, 0, len(pc.Channels))
		knownRoles := map[string]bool{}
		for chi := range pc.Channels {
			pch := &pc.Channels[chi]
			if pch.ID == "" {
				diags = append(diags, decode.Diagnostic{
					Code: "missing_channel_id", Path: fmt.Sprintf("captures[%d].channels[%d].id", ci, chi),
					Severity: "error", Message: "channel id is required",
				})
			}
			if pch.Role != "" {
				if knownRoles[pch.Role] {
					diags = append(diags, decode.Diagnostic{
						Code: "duplicate_role", Path: fmt.Sprintf("captures[%d].channels[%d].role", ci, chi),
						Severity: "error", Message: "duplicate channel role: " + pch.Role,
					})
				}
				knownRoles[pch.Role] = true
			}
			thr := decode.Quantity{Raw: pch.Threshold, Unit: "V"}
			if pch.Threshold != "" {
				q := decode.ParseTimeQuantity(pch.Threshold, "V", "", 0)
				if q.NormalValid {
					thr = q
				} else {
					diags = append(diags, decode.Diagnostic{
						Code:     "unconvertible_threshold",
						Path:     fmt.Sprintf("captures[%d].channels[%d].threshold", ci, chi),
						Severity: "warning", Message: "threshold retained raw: " + pch.Threshold,
					})
				}
			}
			chans = append(chans, decode.ChannelSpec{
				ID: pch.ID, Alias: pch.Alias, Probe: pch.Probe, Role: pch.Role,
				Inverted: pch.Inverted, InitialRaw: pch.InitialRaw, Threshold: thr,
			})
		}
		edges, ediags := buildEdges(pc, ci, params, chans)
		diags = append(diags, ediags...)
		totalEdges += len(edges)
		cid := pc.ID
		if cid == "" {
			cid = "cap_" + decode.CanonicalHash("", pc.Protocol+fmt.Sprint(ci))[4:14]
		}
		caps = append(caps, &decode.Capture{
			ID: cid, Revision: 0, Protocol: pc.Protocol, DecoderVer: ver,
			Params: params, Channels: chans, Edges: edges, Origin: "casepack",
		})
	}
	if p.MaxEdges > 0 && totalEdges > p.MaxEdges {
		return projectID, caps, diags, &decode.BudgetError{What: "edges", Limit: p.MaxEdges}
	}
	if hasError(diags) {
		return projectID, caps, diags, &decode.SemanticError{Diagnostics: diags}
	}
	return projectID, caps, diags, nil
}

func buildParams(pc *PackCapture, ci int) (decode.Params, []decode.Diagnostic) {
	var diags []decode.Diagnostic
	p := pc.Params
	out := decode.Params{
		TimeBase:       p.TimeBase,
		CoordinateBase: p.CoordinateBase,
		ClockPolarity:  p.ClockPolarity,
		ClockPhase:     p.ClockPhase,
		SpiMode:        p.SpiMode,
		BitOrder:       p.BitOrder,
		StretchNS:      p.StretchNS,
		GapNS:          p.GapNS,
		ActiveLevel:    p.ActiveLevel,
		SyncWord:       p.SyncWord,
		SyncBits:       p.SyncBits,
	}
	if p.SampleRate != "" {
		hz, _, ok := decode.ParseRate(p.SampleRate)
		if !ok {
			diags = append(diags, decode.Diagnostic{
				Code: "unconvertible_sample_rate",
				Path: fmt.Sprintf("captures[%d].params.sample_rate", ci), Severity: "warning",
				Message: "sample rate retained raw: " + p.SampleRate,
			})
			out.SampleRate = decode.Quantity{Raw: p.SampleRate, Unit: "Hz"}
		} else {
			out.SampleRate = decode.Quantity{Raw: p.SampleRate, Normal: hz, Unit: "Hz", NormalValid: true}
		}
	}
	if out.BitOrder == "" {
		out.BitOrder = "msb"
	}
	return out, diags
}

func buildEdges(pc *PackCapture, ci int, params decode.Params, chans []decode.ChannelSpec) ([]decode.Edge, []decode.Diagnostic) {
	var diags []decode.Diagnostic
	rate := 0.0
	if params.SampleRate.NormalValid {
		rate = params.SampleRate.Normal
	}
	byID := map[string]bool{}
	edges := make([]decode.Edge, 0, len(pc.Edges))
	for ei := range pc.Edges {
		pe := &pc.Edges[ei]
		q := decode.ParseTimeQuantity(pe.Time, "", params.TimeBase, rate)
		if !q.NormalValid {
			diags = append(diags, decode.Diagnostic{
				Code: "unconvertible_time",
				Path: fmt.Sprintf("captures[%d].edges[%d].t", ci, ei), Severity: "warning",
				Message: "edge time retained raw and excluded from decode: " + pe.Time,
			})
		}
		if pe.Level != 0 && pe.Level != 1 {
			diags = append(diags, decode.Diagnostic{
				Code: "bad_level", Path: fmt.Sprintf("captures[%d].edges[%d].l", ci, ei),
				Severity: "error", Message: fmt.Sprintf("edge level must be 0/1, got %d", pe.Level),
			})
		}
		origin := pe.Origin
		if origin == "" {
			origin = fmt.Sprintf("captures[%d].edges[%d]", ci, ei)
		}
		id := "ed_" + decode.CanonicalHash("", pc.ID+"|"+pe.Channel+"|"+pe.Time+"|"+fmt.Sprint(pe.Level))[4:16]
		if byID[id] {
			id = id + fmt.Sprintf("-%d", ei)
		}
		byID[id] = true
		edges = append(edges, decode.Edge{
			ID: id, Seq: ei, Channel: pe.Channel, Time: q,
			LevelRaw: pe.Level, LevelLogic: pe.Level, Origin: origin,
		})
	}
	for ei := range edges {
		for _, ch := range chans {
			if (ch.ID == edges[ei].Channel || ch.Alias == edges[ei].Channel) && ch.Inverted {
				edges[ei].LevelLogic = 1 - edges[ei].LevelRaw
			}
		}
	}
	return edges, diags
}

func hasError(diags []decode.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == "error" {
			return true
		}
	}
	return false
}

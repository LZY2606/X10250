package importer

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"logicbench/internal/model"
)

// Materialized is the full accepted import payload.
type Materialized struct {
	Import     model.Import
	Channels   []model.Channel
	Edges      []model.Edge
	Probe      model.ProbeConfig
	Diagnostics []model.Diagnostic
}

// Materialize converts a RawDocument into immutable model objects. projectID
// and importID are assigned by the caller (stable across re-imports when an
// archive explicitly carries them).
func Materialize(projectID, importID string, seq int, doc *RawDocument, raw []byte, sha string) *Materialized {
	now := time.Now().UTC()
	m := &Materialized{}
	m.Import = model.Import{
		ID: importID, ProjectID: projectID, Seq: seq,
		FileName: doc.FileName, FormatVer: doc.FormatVer,
		RawSHA256: sha, ByteCount: len(raw), AcceptedAt: now,
	}
	addDiag := func(code, sev, msg string, line int, subject string) {
		d := diag(projectID, importID, code, sev, msg, line)
		d.Subject = subject
		m.Diagnostics = append(m.Diagnostics, d)
	}

	// Channels: "ALIAS / phys=P3 inverted" style.
	byAlias := map[string]model.Channel{}
	for _, rc := range doc.Channels {
		alias, physical, inverted := describeChannel(rc.Alias)
		ch := model.Channel{
			ID: model.DeterministicID(model.KindChannel, importID,
				fmt.Sprintf("%d:%s", rc.Index, alias)),
			ProjectID: projectID, ImportID: importID, Index: rc.Index,
			Alias: alias, Physical: physical, Inverted: inverted,
		}
		m.Channels = append(m.Channels, ch)
		byAlias[alias] = ch
	}

	// Probe config (versioned).
	gapQ := model.ParseTime(doc.Probe.Gap)
	stQ := model.ParseTime(doc.Probe.Stretch)
	probe := model.ProbeConfig{
		ID: model.DeterministicID(model.KindProbe, projectID,
			fmt.Sprintf("%s:v1:%s", importID, sha)),
		ProjectID: projectID, Version: 1,
		Decoder: orDefault(doc.Probe.Decoder, "bench-i2cspi"),
		DecVer:  orDefault(doc.Probe.DecVer, "1.0.0"),
		Bus:     doc.Probe.Bus, Roles: map[string]string{},
		SampleRate: model.ParseRate(doc.Probe.SampleRate),
		GapThresholdRaw: doc.Probe.Gap, StretchTolRaw: doc.Probe.Stretch,
		SyncWord: doc.Probe.SyncWord, CreatedAt: now, Note: doc.Probe.Note,
	}
	if doc.Probe.CPOL != nil {
		probe.CPOL = doc.Probe.CPOL
	}
	if doc.Probe.CPHA != nil {
		probe.CPHA = doc.Probe.CPHA
	}
	if gapQ.Convertible {
		probe.GapPS = gapQ.Value
	} else if doc.Probe.Gap != "" {
		addDiag("bad_gap_unit", "error",
			"gap threshold not convertible: "+doc.Probe.Gap, 0, "")
	}
	if stQ.Convertible {
		probe.StretchTolPS = stQ.Value
	} else if doc.Probe.Stretch != "" {
		addDiag("bad_stretch_unit", "error",
			"stretch tolerance not convertible: "+doc.Probe.Stretch, 0, "")
	}
	for role, alias := range doc.Probe.Roles {
		probe.Roles[normalizeRole(role)] = alias
	}
	m.Probe = probe

	// Edges: preserve raw tokens; normalize time; derive logical level with
	// inversion but keep LevelRaw and the inversion flag.
	for i, re := range doc.Edges {
		ch, ok := byAlias[re.Channel]
		edge := model.Edge{
			ID: model.DeterministicID(model.KindEdge, importID,
				fmt.Sprintf("%d:%s:%s", i, re.Channel, re.TimeRaw)),
			ProjectID: projectID, ImportID: importID,
			ChannelID: ch.ID, Seq: i, TimeRaw: re.TimeRaw, LevelRaw: re.LevelRaw,
			Level: -1,
			Position: model.SourcePosition{File: doc.FileName, Line: re.Line},
		}
		if !ok {
			addDiag("unknown_channel", "error",
				"edge references undeclared channel alias: "+re.Channel, re.Line, edge.ID)
		}
		q := model.ParseTime(re.TimeRaw)
		if q.Convertible {
			edge.TimePS = q.Value
		} else {
			addDiag("bad_time_unit", "error",
				"edge time not convertible, kept in place: "+re.TimeRaw, re.Line, edge.ID)
		}
		rawLevel := strings.ToLower(strings.TrimSpace(re.LevelRaw))
		switch rawLevel {
		case "0", "low", "l":
			edge.Level = 0
		case "1", "high", "h":
			edge.Level = 1
		default:
			addDiag("bad_level", "error",
				"edge level must be 0/1: "+re.LevelRaw, re.Line, edge.ID)
		}
		if ok && ch.Inverted && edge.Level != -1 {
			edge.Inverted = true
			edge.Level ^= 1
		}
		m.Edges = append(m.Edges, edge)
	}
	// stable order: by normalized time when available, file seq otherwise.
	sort.SliceStable(m.Edges, func(a, b int) bool {
		ea, eb := m.Edges[a], m.Edges[b]
		if ea.TimePS != nil && eb.TimePS != nil && *ea.TimePS != *eb.TimePS {
			return *ea.TimePS < *eb.TimePS
		}
		return ea.Seq < eb.Seq
	})
	return m
}

func describeChannel(spec string) (alias, physical string, inverted bool) {
	physical = ""
	rest := spec
	if a, p, ok := strings.Cut(rest, "/"); ok {
		alias = strings.TrimSpace(a)
		rest = strings.TrimSpace(p)
	} else {
		alias = strings.TrimSpace(rest)
		rest = ""
	}
	if rest != "" {
		fields := strings.Fields(rest)
		physical = fields[0]
		for _, f := range fields[1:] {
			if strings.EqualFold(f, "inverted") || f == "~" || f == "!" {
				inverted = true
			}
		}
	}
	return
}

func normalizeRole(r string) string {
	r = strings.ToLower(strings.TrimSpace(r))
	alias := map[string]string{
		"sda": "SDA", "data": "SDA",
		"scl": "SCL", "clock": "SCL", "clk": "SCL", "sclk": "SCLK",
		"mosi": "MOSI", "miso": "MISO",
		"cs": "CS", "ss": "CS",
		"sync": "SYNC",
	}
	if v, ok := alias[r]; ok {
		return v
	}
	return strings.ToUpper(r)
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

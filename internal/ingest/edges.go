package ingest

import (
	"fmt"
	"strings"

	"edgebench/internal/domain"
)

func parseEdges(b *Bundle, res *ParseResult, lim Limits) {
	chans := res.Materials.ChannelByID()
	var hz *int64
	if res.Materials.Capture.SampleRate.Value != nil {
		v := *res.Materials.Capture.SampleRate.Value
		hz = &v
	}
	var lastTime *int64
	for i := range b.Edges {
		be := &b.Edges[i]
		path := fmt.Sprintf("/edges/%d", i)
		if len(res.Materials.Edges)+1 > lim.MaxEdges {
			res.Diags = append(res.Diags, newDiag("budget_edges", domain.SevFatal, "edge budget exhausted", path))
			break
		}
		if chans[be.Chan] == nil {
			res.Diags = append(res.Diags, fatal("edge_channel_missing", "edge references unknown channel "+be.Chan, path+"/chan"))
			continue
		}
		t := domain.Time{}
		if be.Samples != nil {
			t.Raw = fmt.Sprintf("%d", *be.Samples)
			t.Unit = "samples"
			if hz == nil {
				d := newDiag("sample_conversion_missing_rate", domain.SevError,
					"cannot convert sample index without integral sample rate; raw sample index retained", path+"/samples")
				res.Diags = append(res.Diags, d)
				t.DiagID = d.ID
			} else {
				ns := (*be.Samples) * 1_000_000_000 / *hz
				if (*be.Samples)*1_000_000_000%*hz != 0 {
					d := newDiag("sample_conversion_fractional", domain.SevWarn,
						"sample index has no exact nanosecond time; raw index retained, floor used", path+"/samples")
					res.Diags = append(res.Diags, d)
					t.DiagID = d.ID
				}
				t.NS = &ns
			}
		} else {
			t.Raw = be.Raw
			t.Unit = be.Unit
			unit := strings.ToLower(be.Unit)
			mag, ok := atof(be.Raw)
			if !ok {
				d := newDiag("time_unparsable", domain.SevError, "edge time not numeric; raw value retained", path+"/raw")
				res.Diags = append(res.Diags, d)
				t.DiagID = d.ID
			} else if ns, ok := TimeValue(mag, unit); ok {
				t.NS = &ns
			} else {
				d := newDiag("time_unit", domain.SevError, "time unit must be s|ms|us|ns; raw value retained", path+"/unit")
				res.Diags = append(res.Diags, d)
				t.DiagID = d.ID
			}
		}
		rawLevel, d := levelOf(be.Level)
		if d != nil {
			d.Pos.Path = path + "/level"
			res.Diags = append(res.Diags, d)
		}
		e := &domain.Edge{
			Seq: len(res.Materials.Edges), Chan: be.Chan, Time: t,
			RawLevel: be.Level,
		}
		if rawLevel != nil {
			e.Level = logicalLevel(rawLevel, chans[be.Chan].Invert)
		} else {
			e.DiagID = d.ID
		}
		if t.NS != nil {
			if lastTime != nil && *t.NS < *lastTime {
				dd := newDiag("edge_time_nonmonotonic", domain.SevWarn,
					"edge time precedes a previous edge; ordering is normalized during decode", path)
				dd.AtNS = t.NS
				res.Diags = append(res.Diags, dd)
			}
			last := *t.NS
			lastTime = &last
		}
		res.Materials.Edges = append(res.Materials.Edges, e)
	}
}

// assignEdgeIDs gives every edge a deterministic identity derived from
// channel, time and physical level. Equal inputs therefore keep identical
// IDs across export/import round-trips.
func assignEdgeIDs(m *domain.Materials) {
	type sk struct {
		chanID string
		ns     int64
		has    bool
		rawT   string
		rawL   string
	}
	count := map[sk]int{}
	assigned := map[sk]int{}
	for _, e := range m.Edges {
		k := sk{e.Chan, ptrNS(e.Time.NS), e.Time.NS != nil, e.Time.Raw, e.RawLevel}
		count[k]++
	}
	for _, e := range m.Edges {
		k := sk{e.Chan, ptrNS(e.Time.NS), e.Time.NS != nil, e.Time.Raw, e.RawLevel}
		assigned[k]++
		ord := assigned[k]
		var src any
		if k.has {
			src = []any{e.Chan, k.ns, e.RawLevel, ord, count[k]}
		} else {
			src = []any{e.Chan, "raw", k.rawT, e.RawLevel, ord, count[k]}
		}
		e.ID = Digest("edge", src)
	}
}

func ptrNS(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func atof(s string) (float64, bool) {
	var f float64
	var neg bool
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	div := 1.0
	dot := false
	for _, r := range s {
		if r >= '0' && r <= '9' {
			f = f*10 + float64(r-'0')
			if dot {
				div *= 10
			}
		} else if r == '.' && !dot {
			dot = true
		} else {
			return 0, false
		}
	}
	f /= div
	if neg {
		f = -f
	}
	return f, true
}

// OrderedEdges returns edges with usable times sorted by (time, channel,
// original seq). Edges without a normalized time are excluded and an
// error-level diagnostic is emitted; they remain stored in Materials.
func OrderedEdges(m *domain.Materials, diags *[]*domain.Diagnostic) []*domain.Edge {
	var out []*domain.Edge
	for _, e := range m.Edges {
		if e.Time.NS == nil {
			*diags = append(*diags, &domain.Diagnostic{
				ID: "diag_edge_" + e.ID[:8], Code: "edge_excluded_no_time",
				Severity: domain.SevError, Message: "edge excluded from decode: no normalized time",
				Pos: domain.Pos{Path: "/edges"}, AtRaw: e.Time.Raw,
			})
			continue
		}
		if e.Level == nil {
			*diags = append(*diags, &domain.Diagnostic{
				ID: "diag_edge_" + e.ID[5:13], Code: "edge_excluded_no_level",
				Severity: domain.SevError, Message: "edge excluded from decode: level not understood",
				Pos: domain.Pos{Path: "/edges"}, AtNS: e.Time.NS,
			})
			continue
		}
		out = append(out, e)
	}
	sortEdges(out)
	return out
}

func sortEdges(es []*domain.Edge) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := *es[i].Time.NS, *es[j].Time.NS
		if a != b {
			return a < b
		}
		if es[i].Chan != es[j].Chan {
			return es[i].Chan < es[j].Chan
		}
		return es[i].Seq < es[j].Seq
	})
}

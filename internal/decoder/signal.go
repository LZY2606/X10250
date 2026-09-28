package decoder

import (
	"sort"

	"edge-decoder-review/internal/model"
)

type point struct {
	t        float64
	edge     model.Edge
	logical  bool
	physical bool
}

type trace struct {
	alias    string
	points   []point
	inverted bool
}

func buildTraces(c model.Capture) (map[string]*trace, []model.Diagnostic) {
	inv := map[string]bool{}
	for _, ch := range c.Channels {
		inv[ch.Alias] = ch.Inverted
	}
	traces := map[string]*trace{}
	var diags []model.Diagnostic
	for _, e := range c.Edges {
		if !e.Time.Valid || e.After == nil {
			continue
		}
		tr := traces[e.Channel]
		if tr == nil {
			tr = &trace{alias: e.Channel, inverted: inv[e.Channel]}
			traces[e.Channel] = tr
		}
		physical := *e.After
		tr.points = append(tr.points, point{t: e.Time.Value, edge: e, physical: physical, logical: physical != inv[e.Channel]})
	}
	for alias, tr := range traces {
		sort.SliceStable(tr.points, func(i, j int) bool {
			if tr.points[i].t == tr.points[j].t {
				return tr.points[i].edge.ID < tr.points[j].edge.ID
			}
			return tr.points[i].t < tr.points[j].t
		})
		if len(tr.points) > 0 && tr.points[0].logical {
			diags = append(diags, model.Diagnostic{Code: "NO_IDLE_BASELINE", Severity: "warning", Path: "capture.edges", Message: alias + " first observed level is high; inferred idle", EdgeIDs: []string{tr.points[0].edge.ID}})
		}
	}
	return traces, diags
}

func (tr trace) at(t float64) (bool, int) {
	level := false
	idx := -1
	for i, p := range tr.points {
		if p.t > t {
			break
		}
		level = p.logical
		idx = i
	}
	return level, idx
}

func (tr trace) changes() []point { return tr.points }

func edgesBetween(traces map[string]*trace, aliases []string, start, end float64) []string {
	var ids []string
	for _, alias := range aliases {
		tr := traces[alias]
		if tr == nil {
			continue
		}
		for _, p := range tr.points {
			if p.t >= start && p.t <= end {
				ids = append(ids, p.edge.ID)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func boolPtr(v bool) *bool { return &v }

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

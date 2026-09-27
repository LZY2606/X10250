package decode

import (
	"sort"
	"strings"

	"edge-decode-station/internal/domain"
)

type edgeEvent struct {
	time float64
	raw  int
	id   string
}

type signal struct {
	alias    string
	inverted bool
	initial  int
	edges    []edgeEvent
}

type levelPoint struct {
	t     float64
	level int
	id    string
}

func buildSignals(capture domain.Capture) (map[string]*signal, []domain.Diagnostic) {
	signals := map[string]*signal{}
	var diags []domain.Diagnostic
	for _, ch := range capture.Capture_Channels() {
		signals[ch.Alias] = &signal{alias: ch.Alias, inverted: ch.Inverted, initial: ch.InitialRawLevel}
	}
	for _, e := range capture.Edges {
		s := signals[e.Channel]
		if s == nil {
			diags = append(diags, domain.Diagnostic{Code: "unknown_channel", Severity: "error", Path: "edges.channel_alias", Message: "edge references an absent channel alias", Value: e.Channel, Evidence: []string{e.ID}})
			continue
		}
		if !e.Time.Valid {
			diags = append(diags, domain.Diagnostic{Code: "unconvertible_time", Severity: "error", Path: "edges.time", Message: "time cannot be normalized and remains at its original position", Value: e.Time.Raw, Evidence: []string{e.ID}})
			continue
		}
		s.edges = append(s.edges, edgeEvent{t: e.Time.Nanos, raw: e.RawLevel, id: e.ID})
	}
	for _, s := range signals {
		sort.Slice(s.edges, func(i, j int) bool {
			if s.edges[i].t == s.edges[j].t {
				return s.edges[i].id < s.edges[j].id
			}
			return s.edges[i].t < s.edges[j].t
		})
	}
	return signals, diags
}

func (s *signal) normalizedAt(t float64) int {
	level := s.initial
	for _, e := range s.edges {
		if e.t <= t {
			level = e.raw
		} else {
			break
		}
	}
	if s.inverted {
		return level ^ 1
	}
	return level
}

func (s *signal) transitions() []levelPoint {
	out := make([]levelPoint, 0, len(s.edges)+1)
	level := s.initial
	out = append(out, levelPoint{t: -1, level: s.norm(level), id: ""})
	for _, e := range s.edges {
		level = e.raw
		out = append(out, levelPoint{t: e.t, level: s.norm(level), id: e.id})
	}
	return out
}

func (s *signal) norm(raw int) int {
	if s.inverted {
		return raw ^ 1
	}
	return raw
}

func findChannel(capture domain.Capture, alias string) *domain.Channel {
	for i := range capture.Capture_Channels() {
		if capture.Capture_Channels()[i].Alias == alias {
			return &capture.Capture_Channels()[i]
		}
	}
	return nil
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func joinIDs(ids ...[]string) []string {
	var out []string
	for _, xs := range ids {
		out = append(out, xs...)
	}
	return uniqueStrings(out)
}

func basis(capture domain.Capture) string {
	if capture.Origin != "" {
		return capture.Origin
	}
	return "capture_start"
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}

func fragmentAt(capture domain.Capture, t float64) string {
	for _, f := range capture.Fragments {
		if t >= f.Start.Nanos && t <= f.End.Nanos {
			return f.ID
		}
	}
	return ""
}

func fragmentsFor(capture domain.Capture, start, end float64) []string {
	var out []string
	for _, f := range capture.Fragments {
		if f.End.Nanos >= start && f.Start.Nanos <= end {
			out = append(out, f.ID)
		}
	}
	sort.Strings(out)
	return out
}

func edgeIDsInRange(s *signal, start, end float64) []string {
	var out []string
	for _, e := range s.edges {
		if e.t >= start && e.t <= end {
			out = append(out, e.id)
		}
	}
	return uniqueStrings(out)
}

func edgeNear(s *signal, t, window float64) []string {
	var out []string
	for _, e := range s.edges {
		if e.t >= t-window && e.t <= t+window {
			out = append(out, e.id)
		}
	}
	return uniqueStrings(out)
}

func describeSignal(capture domain.Capture, aliases ...string) string {
	parts := []string{}
	for _, alias := range aliases {
		ch := findChannel(capture, alias)
		if ch == nil {
			continue
		}
		inv := "non-inverted"
		if ch.Inverted {
			inv = "inverted"
		}
		parts = append(parts, alias+"="+ch.Name+"/"+inv)
	}
	return strings.Join(parts, ",")
}
